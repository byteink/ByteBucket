package middleware

import (
	"math"
	"testing"
)

func TestHistogramQuantile_Interpolates(t *testing.T) {
	// Cumulative counts over bounds; total = 100. p95 (rank 95) first crosses at
	// the 1.0 bound (count 95), interpolating from (0.5, 50): exactly 1.0.
	buckets := []bucketCount{
		{0.1, 10}, {0.5, 50}, {1.0, 95}, {math.Inf(1), 100},
	}
	if got := histogramQuantile(0.95, buckets); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("p95 = %v, want 1.0", got)
	}
	// p50 (rank 50) crosses at the 0.5 bound exactly.
	if got := histogramQuantile(0.50, buckets); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("p50 = %v, want 0.5", got)
	}
}

func TestHistogramQuantile_EmptyOrZero(t *testing.T) {
	if got := histogramQuantile(0.95, nil); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
	if got := histogramQuantile(0.95, []bucketCount{{0.1, 0}, {math.Inf(1), 0}}); got != 0 {
		t.Fatalf("zero-count = %v, want 0", got)
	}
}

// A quantile landing in the +Inf overflow bucket has no finite upper bound, so
// the best estimate is the last finite bound.
func TestHistogramQuantile_InfBucketUsesPrevBound(t *testing.T) {
	buckets := []bucketCount{{0.1, 10}, {1.0, 50}, {math.Inf(1), 100}}
	if got := histogramQuantile(0.95, buckets); got != 1.0 {
		t.Fatalf("inf-overflow p95 = %v, want 1.0", got)
	}
}

func TestRequestsByStatusClass_GroupsByFirstDigit(t *testing.T) {
	before := RequestsByStatusClass()
	httpRequestsTotal.WithLabelValues("GET", "/agg-test", "200").Inc()
	httpRequestsTotal.WithLabelValues("GET", "/agg-test", "404").Inc()
	httpRequestsTotal.WithLabelValues("GET", "/agg-test", "503").Inc()
	after := RequestsByStatusClass()
	if after["2xx"] < before["2xx"]+1 {
		t.Fatalf("2xx did not increase: %v -> %v", before["2xx"], after["2xx"])
	}
	if after["4xx"] < before["4xx"]+1 || after["5xx"] < before["5xx"]+1 {
		t.Fatalf("4xx/5xx did not increase: %+v", after)
	}
}

func TestMultipartInProgress_ReadsGauge(t *testing.T) {
	MultipartUploadsInProgress.Set(3)
	t.Cleanup(func() { MultipartUploadsInProgress.Set(0) })
	if got := MultipartInProgress(); got != 3 {
		t.Fatalf("multipart gauge = %v, want 3", got)
	}
}
