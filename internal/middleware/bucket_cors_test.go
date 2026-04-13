package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withBucket prepares a bucket dir under a temp ObjectsRoot and seeds an
// optional CORS config. Returns the bucket name.
func withBucket(t *testing.T, cfg *storage.BucketCORSConfig) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	orig := storage.ObjectsRoot
	storage.ObjectsRoot = dir
	t.Cleanup(func() { storage.ObjectsRoot = orig })
	bucket := "b1"
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if cfg != nil {
		if err := storage.PutBucketCORS(bucket, cfg); err != nil {
			t.Fatalf("seed cors: %v", err)
		}
	}
	return bucket
}

func newEngine() *gin.Engine {
	r := gin.New()
	r.Use(BucketCORSMiddleware())
	r.Any("/*any", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestBucketCORS_MatchEmitsHeaders(t *testing.T) {
	withBucket(t, &storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"GET", "PUT"},
		AllowedOrigins: []string{"https://example.com"},
		AllowedHeaders: []string{"*"},
		ExposeHeaders:  []string{"ETag"},
		MaxAgeSeconds:  600,
	}}})

	req := httptest.NewRequest(http.MethodOptions, "/b1/obj", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: expected 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("ACAO: got %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "ETag" {
		t.Fatalf("Expose: got %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("MaxAge: got %q", got)
	}
}

func TestBucketCORS_NonMatchingOriginEmitsNoHeaders(t *testing.T) {
	withBucket(t, &storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"https://allowed.example"},
	}}})

	req := httptest.NewRequest(http.MethodGet, "/b1/obj", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no ACAO, got %q", got)
	}
	// Non-preflight GET still reaches the handler; only preflights are blocked.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for actual GET, got %d", w.Code)
	}
}

func TestBucketCORS_PreflightNonMatchingOriginIs403(t *testing.T) {
	withBucket(t, &storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"https://allowed.example"},
	}}})

	req := httptest.NewRequest(http.MethodOptions, "/b1/obj", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 preflight rejection, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no ACAO, got %q", got)
	}
}

func TestBucketCORS_NoConfigPreflightIs403(t *testing.T) {
	withBucket(t, nil)

	req := httptest.NewRequest(http.MethodOptions, "/b1/obj", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for CORS-less bucket, got %d", w.Code)
	}
}

func TestBucketCORS_NoOriginPassesThrough(t *testing.T) {
	withBucket(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/b1/obj", nil)
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected ACAO on non-browser request: %q", got)
	}
}

func TestBucketCORS_WildcardOrigin(t *testing.T) {
	withBucket(t, &storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"https://*.example.com"},
	}}})

	req := httptest.NewRequest(http.MethodGet, "/b1/obj", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	newEngine().ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("wildcard origin: got %q", got)
	}
}

func TestBucketFromPath(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"/":               "",
		"/b1":             "b1",
		"/b1/":            "b1",
		"/b1/obj":         "b1",
		"/b1/obj/nested":  "b1",
		"no-leading-slash": "no-leading-slash",
	}
	for in, want := range cases {
		if got := bucketFromPath(in); got != want {
			t.Errorf("bucketFromPath(%q) = %q want %q", in, got, want)
		}
	}
}
