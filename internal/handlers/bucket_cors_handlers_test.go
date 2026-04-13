package handlers

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// newCORSTestEngine builds a minimal router that wires the per-bucket CORS
// handlers with query-subresource dispatch. It intentionally does not
// include any auth middleware so tests target the handlers in isolation.
func newCORSTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["cors"]; ok {
			PutBucketCORSHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.GET("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["cors"]; ok {
			GetBucketCORSHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.DELETE("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["cors"]; ok {
			DeleteBucketCORSHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

// withBucketDir points storage.ObjectsRoot at a temp dir containing the
// named bucket so the persistence layer has somewhere to write.
func withBucketDir(t *testing.T, bucket string) {
	t.Helper()
	dir := t.TempDir()
	orig := storage.ObjectsRoot
	storage.ObjectsRoot = dir
	t.Cleanup(func() { storage.ObjectsRoot = orig })
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
}

func TestPutGetCORS_XMLRoundTrip(t *testing.T) {
	withBucketDir(t, "b1")

	body := []byte(`<CORSConfiguration>
  <CORSRule>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedMethod>PUT</AllowedMethod>
    <AllowedOrigin>https://example.com</AllowedOrigin>
    <AllowedHeader>*</AllowedHeader>
    <ExposeHeader>ETag</ExposeHeader>
    <MaxAgeSeconds>3000</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>`)

	r := newCORSTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1?cors", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// GET on the SigV4 surface returns XML; path does not start with /s3,
	// so wantsJSON is false unless the Accept header opts in.
	req = httptest.NewRequest(http.MethodGet, "/b1?cors", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var decoded s3CORSConfiguration
	if err := xml.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal xml: %v body=%s", err, w.Body.String())
	}
	if len(decoded.CORSRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(decoded.CORSRules))
	}
	rule := decoded.CORSRules[0]
	if !equalStrings(rule.AllowedMethod, []string{"GET", "PUT"}) {
		t.Errorf("methods: got %v", rule.AllowedMethod)
	}
	if !equalStrings(rule.AllowedOrigin, []string{"https://example.com"}) {
		t.Errorf("origins: got %v", rule.AllowedOrigin)
	}
	if rule.MaxAgeSeconds != 3000 {
		t.Errorf("max age: got %d", rule.MaxAgeSeconds)
	}
}

func TestPutGetCORS_JSONRoundTripOnAdminPath(t *testing.T) {
	withBucketDir(t, "b1")

	payload := storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"POST"},
		AllowedOrigins: []string{"https://admin.ui"},
		MaxAgeSeconds:  60,
	}}}
	jsonBody, _ := json.Marshal(payload)

	// Build a router rooted at /s3 so wantsJSON activates on the path prefix.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s3 := r.Group("/s3")
	s3.PUT("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["cors"]; ok {
			PutBucketCORSHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	s3.GET("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["cors"]; ok {
			GetBucketCORSHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPut, "/s3/b1?cors", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/s3/b1?cors", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		t.Fatalf("expected JSON body on admin surface, got %s", w.Body.String())
	}
	var got storage.BucketCORSConfig
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal json: %v body=%s", err, w.Body.String())
	}
	if len(got.CORSRules) != 1 || got.CORSRules[0].AllowedOrigins[0] != "https://admin.ui" {
		t.Fatalf("json round-trip mismatch: %+v", got)
	}
}

func TestGetCORS_MissingReturns404(t *testing.T) {
	withBucketDir(t, "b1")

	r := newCORSTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/b1?cors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "NoSuchCORSConfiguration") {
		t.Fatalf("expected NoSuchCORSConfiguration, got %s", w.Body.String())
	}
}

func TestDeleteCORS_RemovesConfig(t *testing.T) {
	withBucketDir(t, "b1")

	if err := storage.PutBucketCORS("b1", &storage.BucketCORSConfig{CORSRules: []storage.BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"*"},
	}}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := newCORSTestEngine()
	req := httptest.NewRequest(http.MethodDelete, "/b1?cors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/b1?cors", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d body=%s", w.Code, w.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
