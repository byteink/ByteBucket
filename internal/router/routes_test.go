package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// routeExists reports whether the router has a route registered for
// (method, path). The Gin router exposes this via Routes() which walks the
// full route table.
func routeExists(r *gin.Engine, method, path string) bool {
	for _, info := range r.Routes() {
		if info.Method == method && info.Path == path {
			return true
		}
	}
	return false
}

// The storage router must register every bucket/object verb. A regression
// here would silently break S3 clients; assert the full surface rather than
// a spot check.
func TestStorageRouterRegistersS3Surface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewStorageRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}))

	cases := []struct{ method, path string }{
		{"GET", "/"},
		{"PUT", "/:bucket"},
		{"GET", "/:bucket"},
		{"DELETE", "/:bucket"},
		{"HEAD", "/:bucket"},
		{"PUT", "/:bucket/*objectKey"},
		{"GET", "/:bucket/*objectKey"},
		{"DELETE", "/:bucket/*objectKey"},
		{"HEAD", "/:bucket/*objectKey"},
	}
	for _, tc := range cases {
		if !routeExists(r, tc.method, tc.path) {
			t.Errorf("storage router missing %s %s", tc.method, tc.path)
		}
	}
}

// The storage surface must serve a favicon so a browser probing the storage
// origin does not hit the /:bucket dispatcher and 400 on the invalid bucket
// name. The dedicated route must intercept before that path. With a built UI
// bundle it returns the icon (200); with CI's unbuilt dist (only .keep) it is a
// clean 404 — never the bucket-name 400.
func TestStorageRouterServesFavicon(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewStorageRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}))

	if !routeExists(r, "GET", "/favicon.ico") {
		t.Fatal("storage router missing GET /favicon.ico")
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Fatalf("favicon status = %d, want 200 (built) or 404 (unbuilt) — not the bucket 400", w.Code)
	}
	if w.Code == http.StatusOK {
		if ct := w.Header().Get("Content-Type"); ct != "image/vnd.microsoft.icon" {
			t.Fatalf("favicon content-type = %q, want image/vnd.microsoft.icon", ct)
		}
		if w.Body.Len() == 0 {
			t.Fatal("favicon served an empty body")
		}
	}
}

// The admin router must mount the entire storage surface under /api/s3 so
// the admin UI can manage buckets and objects without re-implementing them,
// and without colliding with the SPA's client-side routes.
func TestAdminRouterMountsStorageUnderAPIS3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewAdminRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}))

	cases := []struct{ method, path string }{
		{"GET", "/api/s3/"},
		{"PUT", "/api/s3/:bucket"},
		{"GET", "/api/s3/:bucket"},
		{"DELETE", "/api/s3/:bucket"},
		{"HEAD", "/api/s3/:bucket"},
		{"PUT", "/api/s3/:bucket/*objectKey"},
		{"GET", "/api/s3/:bucket/*objectKey"},
		{"DELETE", "/api/s3/:bucket/*objectKey"},
		{"HEAD", "/api/s3/:bucket/*objectKey"},
	}
	for _, tc := range cases {
		if !routeExists(r, tc.method, tc.path) {
			t.Errorf("admin router missing %s %s", tc.method, tc.path)
		}
	}
}
