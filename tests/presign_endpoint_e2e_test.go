package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestE2E_PresignEndpoint_WorksWithoutPublicBaseURL verifies the admin
// ?presign convenience endpoint mints a URL even though the container sets no
// PUBLIC_BASE_URL: the server falls back to the localhost storage default
// instead of returning 503. Regression guard for the localhost UX bug.
func TestE2E_PresignEndpoint_WorksWithoutPublicBaseURL(t *testing.T) {
	const bucket = "presign-e2e-bkt"
	resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket, nil, ""))
	_ = resp.Body.Close()
	resp = adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/o.txt", []byte("hi"), "text/plain"))
	_ = resp.Body.Close()
	t.Cleanup(func() {
		r := adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket+"/o.txt", nil, ""))
		_ = r.Body.Close()
		r = adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, ""))
		_ = r.Body.Close()
	})

	resp = adminDo(t, adminRequest(t, http.MethodGet, "/api/s3/"+bucket+"/o.txt?presign&expires=300", nil, ""))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("presign endpoint: got %d, want 200 (no 503 without PUBLIC_BASE_URL). body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Falls back to the localhost storage origin and carries a real signature.
	if !strings.HasPrefix(out.URL, "http://localhost:9000/"+bucket+"/o.txt") {
		t.Fatalf("unexpected presign base: %s", out.URL)
	}
	if !strings.Contains(out.URL, "X-Amz-Signature=") {
		t.Fatalf("presigned URL missing signature: %s", out.URL)
	}
}
