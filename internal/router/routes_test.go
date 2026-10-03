package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	r := NewStorageRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}), middleware.NewIPBanController(middleware.DefaultIPBanConfig()))

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
	r := NewStorageRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}), middleware.NewIPBanController(middleware.DefaultIPBanConfig()))

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

// Login and logout must sit outside the admin auth middleware (they are how a
// browser obtains and drops a session), while the session probe sits behind
// it so it reports 401 once the cookie is gone.
func TestAdminRouterMountsSessionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewAdminRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}))

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/login", http.StatusBadRequest},
		{http.MethodPost, "/api/logout", http.StatusNoContent},
		{http.MethodGet, "/api/session", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if !routeExists(r, tc.method, tc.path) {
			t.Fatalf("admin router missing %s %s", tc.method, tc.path)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

// The failed-auth ban must be wired into the storage chain ahead of SigV4
// auth: auth produces the 401 that counts, and the next request from the same
// public IP is refused with AccessDenied before auth runs again.
func TestStorageRouterMountsIPBan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ban := middleware.NewIPBanController(middleware.IPBanConfig{Enabled: true, MaxFailures: 1, WindowSeconds: 60, BanSeconds: 60})
	r := NewStorageRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}), ban)

	do := func(peer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/nobucket/backup.sql", nil)
		req.RemoteAddr = peer + ":4000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := do("203.0.113.9"); w.Code != http.StatusUnauthorized {
		t.Fatalf("first anonymous request = %d, want 401 from auth", w.Code)
	}
	w := do("203.0.113.9")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatalf("banned request = %d %s, want 403 AccessDenied", w.Code, w.Body.String())
	}
	if w := do("10.0.0.9"); w.Code != http.StatusUnauthorized {
		t.Fatalf("private peer = %d, want 401", w.Code)
	}
	if w := do("10.0.0.9"); w.Code != http.StatusUnauthorized {
		t.Fatalf("private peer banned: %d", w.Code)
	}
}

// The ban config is admin-managed at /api/config/ipban, behind admin auth.
func TestAdminRouterMountsIPBanConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewAdminRouter(middleware.NewRateLimitController(middleware.RateLimitConfig{}))
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if !routeExists(r, m, "/api/config/ipban") {
			t.Fatalf("admin router missing %s /api/config/ipban", m)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(m, "/api/config/ipban", nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s /api/config/ipban = %d, want 401", m, w.Code)
		}
	}
}
