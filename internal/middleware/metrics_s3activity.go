package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// RecordObjectUpload counts one upload (or server-side copy) of n bytes into
// bucket. Called from the object write path, not the HTTP middleware, so it
// reflects real storage activity regardless of which surface served it.
func RecordObjectUpload(bucket string, n int64) {
	s3OperationsTotal.WithLabelValues(bucket, "upload").Inc()
	if n > 0 {
		s3BytesTransferredTotal.WithLabelValues(bucket, "in").Add(float64(n))
	}
}

// RecordObjectDownload counts one download of n bytes from bucket.
func RecordObjectDownload(bucket string, n int64) {
	s3OperationsTotal.WithLabelValues(bucket, "download").Inc()
	if n > 0 {
		s3BytesTransferredTotal.WithLabelValues(bucket, "out").Add(float64(n))
	}
}

// RecordObjectDelete counts one delete request against bucket.
func RecordObjectDelete(bucket string) {
	s3OperationsTotal.WithLabelValues(bucket, "delete").Inc()
}

// BucketActivity is the per-bucket object-operation rollup for the dashboard.
type BucketActivity struct {
	Uploads   float64 `json:"uploads"`
	Downloads float64 `json:"downloads"`
	Deletes   float64 `json:"deletes"`
	BytesIn   float64 `json:"bytesIn"`
	BytesOut  float64 `json:"bytesOut"`
}

// S3ActivityByBucket aggregates the operation and byte counters into a per-bucket
// map. Buckets with any recorded activity appear even if they were later
// deleted, since the counters are cumulative — that is the intended "what has
// happened" view.
func S3ActivityByBucket() map[string]*BucketActivity {
	out := map[string]*BucketActivity{}
	get := func(bucket string) *BucketActivity {
		a := out[bucket]
		if a == nil {
			a = &BucketActivity{}
			out[bucket] = a
		}
		return a
	}

	forEach(s3OperationsTotal, func(labels map[string]string, v float64) {
		a := get(labels["bucket"])
		switch labels["operation"] {
		case "upload":
			a.Uploads += v
		case "download":
			a.Downloads += v
		case "delete":
			a.Deletes += v
		}
	})
	forEach(s3BytesTransferredTotal, func(labels map[string]string, v float64) {
		a := get(labels["bucket"])
		switch labels["direction"] {
		case "in":
			a.BytesIn += v
		case "out":
			a.BytesOut += v
		}
	})
	return out
}

// RequestOutcomes is the S3-surface request-health rollup: response counts by
// status class. It powers the dashboard's 2xx/4xx/5xx view.
type RequestOutcomes struct {
	Success     float64 `json:"success"`     // 2xx
	Redirect    float64 `json:"redirect"`    // 3xx (e.g. 304 conditional)
	ClientError float64 `json:"clientError"` // 4xx
	ServerError float64 `json:"serverError"` // 5xx
}

// S3RequestOutcome counts each S3-surface response by status class. Mounted on
// the data-plane router (:9000) and the admin /api/s3 group only, so admin
// polling, /metrics, /health and SPA assets never inflate request-health.
func S3RequestOutcome() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		switch c.FullPath() {
		case "", "/health", "/favicon.ico":
			// Unmatched route, orchestrator probe, or favicon probe — not bucket traffic.
			return
		}
		s3RequestsTotal.WithLabelValues(statusClass(c.Writer.Status())).Inc()
	}
}

// statusClass maps an HTTP status code to its "Nxx" class label.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// S3RequestOutcomes aggregates the per-class request counters into the
// dashboard rollup.
func S3RequestOutcomes() RequestOutcomes {
	var o RequestOutcomes
	forEach(s3RequestsTotal, func(labels map[string]string, v float64) {
		switch labels["class"] {
		case "2xx":
			o.Success += v
		case "3xx":
			o.Redirect += v
		case "4xx":
			o.ClientError += v
		case "5xx":
			o.ServerError += v
		}
	})
	return o
}

// MultipartInProgress returns the live multipart-uploads gauge value — a real
// bucket-activity signal (uploads currently open).
func MultipartInProgress() float64 {
	var m dto.Metric
	if err := MultipartUploadsInProgress.Write(&m); err != nil || m.Gauge == nil {
		return 0
	}
	return m.Gauge.GetValue()
}

// forEach collects a CounterVec and invokes fn with each series' labels and
// value, centralising the dto plumbing the aggregators share.
func forEach(cv *prometheus.CounterVec, fn func(labels map[string]string, value float64)) {
	ch := make(chan prometheus.Metric, 1024)
	cv.Collect(ch)
	close(ch)
	var m dto.Metric
	for metric := range ch {
		if err := metric.Write(&m); err != nil || m.Counter == nil {
			continue
		}
		labels := make(map[string]string, len(m.Label))
		for _, l := range m.Label {
			labels[l.GetName()] = l.GetValue()
		}
		fn(labels, m.Counter.GetValue())
	}
}
