package tests

import (
	"net/http"
	"testing"
)

// TestE2E_ConditionalRequests drives precondition handling against the running
// container over raw HTTP so the exact status codes (304 / 412) are asserted.
// gin defers WriteHeader, so an empty 304 can regress to 200 if the handler
// stops relying on explicit flushing — this guards that.
func TestE2E_ConditionalRequests(t *testing.T) {
	const bucket = "cond-e2e-bkt"
	const key = "obj.txt"

	resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket, nil, ""))
	_ = resp.Body.Close()
	resp = adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/"+key, []byte("conditional"), "text/plain"))
	etag := resp.Header.Get("ETag")
	_ = resp.Body.Close()
	if etag == "" {
		t.Fatal("PUT did not return an ETag")
	}
	t.Cleanup(func() {
		r := adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket+"/"+key, nil, ""))
		_ = r.Body.Close()
		r = adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, ""))
		_ = r.Body.Close()
	})

	// GET If-None-Match with the current ETag -> 304.
	req := adminRequest(t, http.MethodGet, "/api/s3/"+bucket+"/"+key, nil, "")
	req.Header.Set("If-None-Match", etag)
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match current ETag: got %d want 304", resp.StatusCode)
	}

	// GET If-Match with a stale ETag -> 412.
	req = adminRequest(t, http.MethodGet, "/api/s3/"+bucket+"/"+key, nil, "")
	req.Header.Set("If-Match", `"deadbeef"`)
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match stale ETag: got %d want 412", resp.StatusCode)
	}

	// PUT If-None-Match: * onto the existing key -> 412 (create-only guard).
	req = adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/"+key, []byte("v2"), "text/plain")
	req.Header.Set("If-None-Match", "*")
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match:* on existing: got %d want 412", resp.StatusCode)
	}
}
