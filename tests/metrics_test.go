package tests

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// scrapeMetrics performs a single scrape against the admin port and returns
// the text body. Any non-200 or transport error is a test failure — the
// metrics endpoint is a hard contract that operators grep against.
func scrapeMetrics(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(adminURL + "/metrics")
	if err != nil {
		t.Fatalf("failed to scrape /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", resp.StatusCode)
	}
	// Prometheus text exposition format is text/plain with a version
	// parameter; a bare "text/plain" substring check is sufficient here
	// and avoids pinning to a specific promhttp version.
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain content-type, got %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read /metrics body: %v", err)
	}
	return string(body)
}

// extractCounter parses a single Prometheus text-format sample line and
// returns its numeric value. Returns -1 if the line is missing so callers
// can distinguish "not yet observed" from "observed as zero".
func extractCounter(body, metric, labels string) float64 {
	// Matches e.g. `http_requests_total{method="GET",path="/s3/",status="200"} 7`
	re := regexp.MustCompile("(?m)^" + regexp.QuoteMeta(metric+"{"+labels+"}") + `\s+([0-9eE+\-.]+)\s*$`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return -1
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return -1
	}
	return v
}

// The metrics endpoint must answer 200 with a Prometheus text body that
// exposes the ByteBucket gauges and the standard process collector. We
// deliberately assert on metrics that are always emitted (not on request
// counters that only materialise after the first observation).
func TestMetricsEndpointExposesExpectedMetrics(t *testing.T) {
	body := scrapeMetrics(t)
	for _, needle := range []string{
		"bytebucket_multipart_uploads_in_progress",
		"go_goroutines",
		"process_start_time_seconds",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected %q in /metrics body", needle)
		}
	}
}

// An instrumented request on the admin surface must increment
// http_requests_total for its exact label tuple. Covers the end-to-end
// middleware wiring — not just registration — against the running binary.
func TestMetricsCounterIncrementsAcrossAdminRequest(t *testing.T) {
	before := scrapeMetrics(t)
	labels := `method="GET",path="/s3/",status="200"`
	prev := extractCounter(before, "http_requests_total", labels)
	if prev < 0 {
		prev = 0
	}

	// List buckets on the admin S3 surface. Any authenticated admin call
	// will do; /s3/ is the cheapest.
	req, err := http.NewRequest(http.MethodGet, adminURL+"/s3/", nil)
	if err != nil {
		t.Fatalf("request build: %v", err)
	}
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /s3/, got %d", resp.StatusCode)
	}

	after := scrapeMetrics(t)
	now := extractCounter(after, "http_requests_total", labels)
	if now <= prev {
		t.Fatalf("expected counter to increase past %v, got %v", prev, now)
	}
}
