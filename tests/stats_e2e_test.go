package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestE2E_Stats drives the admin dashboard stats endpoint against the running
// container: after creating a bucket and uploading an object, the summary must
// reflect at least that bucket/object/byte count and a positive request total.
func TestE2E_Stats(t *testing.T) {
	const bucket = "stats-e2e-bkt"
	resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket, nil, ""))
	_ = resp.Body.Close()
	resp = adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/o.txt", []byte("hello"), "text/plain"))
	_ = resp.Body.Close()
	t.Cleanup(func() {
		r := adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket+"/o.txt", nil, ""))
		_ = r.Body.Close()
		r = adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, ""))
		_ = r.Body.Close()
	})

	resp = adminDo(t, adminRequest(t, http.MethodGet, "/api/stats", nil, ""))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET stats: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var s struct {
		Buckets       int64              `json:"buckets"`
		Objects       int64              `json:"objects"`
		Bytes         int64              `json:"bytes"`
		Requests      float64            `json:"requests"`
		StatusClasses map[string]float64 `json:"statusClasses"`
		P95LatencyMs  float64            `json:"p95LatencyMs"`
		TopBuckets    []struct {
			Name  string `json:"name"`
			Bytes int64  `json:"bytes"`
		} `json:"topBuckets"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	// Other tests share the container, so use >= rather than exact counts.
	if s.Buckets < 1 || s.Objects < 1 || s.Bytes < 5 || s.Requests <= 0 {
		t.Fatalf("stats too low: %+v", s)
	}
	// The richer dashboard fields must populate: at least one 2xx (this request
	// chain), a non-negative p95, and our seeded bucket in the top list.
	if s.StatusClasses["2xx"] <= 0 {
		t.Fatalf("expected 2xx requests counted, got %+v", s.StatusClasses)
	}
	if s.P95LatencyMs < 0 {
		t.Fatalf("p95 latency negative: %v", s.P95LatencyMs)
	}
	found := false
	for _, b := range s.TopBuckets {
		if b.Name == bucket {
			found = true
		}
	}
	if !found {
		t.Fatalf("seeded bucket %q not in topBuckets %+v", bucket, s.TopBuckets)
	}
}
