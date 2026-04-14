package router

import (
	"testing"

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
	r := NewStorageRouter()

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

// The admin router must mount the entire storage surface under /api/s3 so
// the admin UI can manage buckets and objects without re-implementing them,
// and without colliding with the SPA's client-side routes.
func TestAdminRouterMountsStorageUnderAPIS3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewAdminRouter()

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
