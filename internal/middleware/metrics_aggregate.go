package middleware

import (
	"math"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// RequestsByStatusClass groups the cumulative request counter by HTTP status
// class (2xx/3xx/4xx/5xx). Used by the admin dashboard to show error rate at a
// glance instead of a single opaque total. Unknown/empty statuses are ignored.
func RequestsByStatusClass() map[string]float64 {
	out := map[string]float64{"2xx": 0, "3xx": 0, "4xx": 0, "5xx": 0}
	ch := make(chan prometheus.Metric, 1024)
	httpRequestsTotal.Collect(ch)
	close(ch)
	var m dto.Metric
	for metric := range ch {
		if err := metric.Write(&m); err != nil || m.Counter == nil {
			continue
		}
		status := ""
		for _, l := range m.Label {
			if l.GetName() == "status" {
				status = l.GetValue()
				break
			}
		}
		if len(status) == 0 {
			continue
		}
		class := string(status[0]) + "xx"
		if _, known := out[class]; known {
			out[class] += m.Counter.GetValue()
		}
	}
	return out
}

// MultipartInProgress returns the live multipart-uploads gauge value.
func MultipartInProgress() float64 {
	var m dto.Metric
	if err := MultipartUploadsInProgress.Write(&m); err != nil || m.Gauge == nil {
		return 0
	}
	return m.Gauge.GetValue()
}

// RequestLatencyP95Seconds estimates the 95th-percentile request latency across
// every route by aggregating the duration histogram's buckets (all series share
// the same bounds) and interpolating within the bucket that crosses the 95%
// mark — the same approach Prometheus' histogram_quantile uses. Returns 0 when
// no requests have been observed.
func RequestLatencyP95Seconds() float64 {
	return histogramQuantile(0.95, aggregateDurationBuckets())
}

// bucketCount pairs a histogram bucket's upper bound with its cumulative count.
type bucketCount struct {
	upper float64
	count uint64
}

// aggregateDurationBuckets sums the per-bound cumulative counts across all
// label series of the duration histogram into a single ordered slice.
func aggregateDurationBuckets() []bucketCount {
	sums := map[float64]uint64{}
	ch := make(chan prometheus.Metric, 1024)
	httpRequestDuration.Collect(ch)
	close(ch)
	var m dto.Metric
	for metric := range ch {
		if err := metric.Write(&m); err != nil || m.Histogram == nil {
			continue
		}
		for _, b := range m.Histogram.Bucket {
			sums[b.GetUpperBound()] += b.GetCumulativeCount()
		}
	}
	out := make([]bucketCount, 0, len(sums))
	for ub, c := range sums {
		out = append(out, bucketCount{upper: ub, count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].upper < out[j].upper })
	return out
}

// histogramQuantile interpolates quantile q (0..1) from ordered cumulative
// buckets. Mirrors Prometheus' algorithm: find the bucket where the cumulative
// count crosses q*total, then linearly interpolate within it.
func histogramQuantile(q float64, buckets []bucketCount) float64 {
	if len(buckets) == 0 {
		return 0
	}
	total := buckets[len(buckets)-1].count
	if total == 0 {
		return 0
	}
	rank := q * float64(total)
	var prevUpper float64
	var prevCount uint64
	for _, b := range buckets {
		if float64(b.count) >= rank {
			// +Inf bucket has no finite upper bound to interpolate to; report
			// the previous finite bound as the best available estimate.
			if math.IsInf(b.upper, 1) {
				return prevUpper
			}
			span := b.upper - prevUpper
			within := b.count - prevCount
			if within == 0 {
				return b.upper
			}
			return prevUpper + span*((rank-float64(prevCount))/float64(within))
		}
		prevUpper = b.upper
		prevCount = b.count
	}
	return buckets[len(buckets)-1].upper
}
