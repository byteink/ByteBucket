package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// httpDurationBuckets covers the full envelope from microsecond-level
// health-check responses up to a 30-second upload of a large multipart part.
// Using an explicit set rather than prometheus.DefBuckets pins the bucket
// boundaries to ByteBucket's own latency profile; the Prom defaults top out
// at 10s which would lump every slow object write into +Inf.
var httpDurationBuckets = []float64{
	.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30,
}

// Size buckets stretch from small S3 metadata requests to the 5 GiB single-
// object PUT ceiling. Exponential spacing keeps the bucket count modest while
// still resolving useful percentiles across the range.
var httpSizeBuckets = prometheus.ExponentialBucketsRange(256, 5<<30, 12)

var (
	// httpRequestsTotal is the primary request-rate / error-rate counter.
	// path is c.FullPath() (the registered template) — using the raw path
	// would let a client walk arbitrary keys and explode label cardinality,
	// breaking Prometheus ingest.
	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests processed, labelled by method, route template and status.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request handling latency, labelled by method and route template.",
		Buckets: httpDurationBuckets,
	}, []string{"method", "path"})

	httpRequestSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_size_bytes",
		Help:    "HTTP request body size in bytes.",
		Buckets: httpSizeBuckets,
	}, []string{"method", "path"})

	httpResponseSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_response_size_bytes",
		Help:    "HTTP response body size in bytes.",
		Buckets: httpSizeBuckets,
	}, []string{"method", "path"})

	// MultipartUploadsInProgress tracks live multipart sessions. Incremented
	// on Create, decremented on Complete or Abort; exposed as an exported
	// var so the handlers package can mutate it without reaching through a
	// setter. A gauge is the right shape because the value moves in both
	// directions and is inherently a snapshot.
	MultipartUploadsInProgress = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bytebucket_multipart_uploads_in_progress",
		Help: "Number of multipart uploads currently open (incremented on Create, decremented on Complete/Abort).",
	})

	// ObjectsBytesTotal is a coarse byte-count signal per bucket. It is a
	// best-effort delta — updated on PutObject / DeleteObject events — and
	// is not recomputed from disk at startup. Operators wanting ground
	// truth should run a reconciliation job; this metric is for trendlines,
	// not accounting.
	ObjectsBytesTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bytebucket_objects_bytes_total",
		Help: "Total bytes of objects per bucket (best-effort, delta-updated; not recomputed at startup).",
	}, []string{"bucket"})
)

func init() {
	// MustRegister panics on duplicate registration, which here would mean a
	// second init ran — the right failure mode for a programming error.
	prometheus.MustRegister(
		httpRequestsTotal,
		httpRequestDuration,
		httpRequestSize,
		httpResponseSize,
		MultipartUploadsInProgress,
		ObjectsBytesTotal,
	)
	// Go runtime and process-level metrics (go_*, process_*) power every
	// standard Prometheus dashboard. The default registry installs both
	// automatically in newer client_golang releases, so Register reports
	// AlreadyRegisteredError — which we tolerate — while older releases
	// need an explicit Register call here. tryRegister keeps us working
	// across both without an upstream-version check.
	tryRegister(collectors.NewGoCollector())
	tryRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// tryRegister registers c unless an identical collector is already present.
// Any other error is fatal: we are at init time and a partially-observable
// process is worse than failing loudly.
func tryRegister(c prometheus.Collector) {
	if err := prometheus.Register(c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return
		}
		panic(err)
	}
}

// Metrics returns a Gin middleware that observes every request at the end of
// handling. The /metrics endpoint itself is explicitly excluded to avoid a
// feedback loop where scrapes inflate their own counters.
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" || path == "/metrics" {
			// No route match or the scrape endpoint — skip to keep the
			// unmatched-path label space empty and the scrape idempotent.
			return
		}
		method := c.Request.Method
		status := strconv.Itoa(c.Writer.Status())
		elapsed := time.Since(start).Seconds()

		httpRequestsTotal.WithLabelValues(method, path, status).Inc()
		httpRequestDuration.WithLabelValues(method, path).Observe(elapsed)
		if c.Request.ContentLength > 0 {
			httpRequestSize.WithLabelValues(method, path).Observe(float64(c.Request.ContentLength))
		}
		if size := c.Writer.Size(); size > 0 {
			httpResponseSize.WithLabelValues(method, path).Observe(float64(size))
		}
	}
}

// PrometheusHandler returns the canonical promhttp handler. Exposed as a
// function rather than a var so callers do not accidentally share the same
// instance across tests that reset the registry.
func PrometheusHandler() http.Handler {
	return promhttp.Handler()
}
