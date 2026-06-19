package handlers

import (
	"errors"
	"net/http"
	"testing"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

var errInjectedStoreFault = errors.New("injected config-store fault")

// TestConfigHandlers_PersistFailures covers the storage write/read error branch
// in every settings handler: a BoltDB failure must surface as a 500, never a
// silent success that returns the new value while having persisted nothing.
func TestConfigHandlers_PersistFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	storage.SetTrustedProxy(storage.TrustedProxyConfig{})
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })

	ctrl := middleware.NewRateLimitController(middleware.RateLimitConfig{})
	SetRateLimitController(ctrl, middleware.RateLimitConfig{})

	cases := []struct {
		name    string
		method  string
		handler gin.HandlerFunc
		body    string
	}{
		{"trustedproxy put", http.MethodPut, PutTrustedProxyHandler, `{"headers":["CF-Connecting-IP"]}`},
		{"accesslog put", http.MethodPut, PutAccessLogHandler, `{"enabled":true,"maxEvents":10,"maxAgeDays":7}`},
		{"retention put", http.MethodPut, PutRetentionHandler, `{"days":7}`},
		{"sync put", http.MethodPut, PutSyncWritesHandler, `{"enabled":true}`},
		{"ratelimit put", http.MethodPut, PutRateLimitHandler, `{"enabled":true,"rps":5,"burst":5}`},
		{"ratelimit get", http.MethodGet, GetRateLimitHandler, ""},
		{"ratelimit delete", http.MethodDelete, DeleteRateLimitHandler, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := storage.SetConfigStoreFaultForTest(errInjectedStoreFault)
			defer restore()
			r := gin.New()
			r.Handle(tc.method, "/x", tc.handler)
			w := doReq(r, tc.method, "/x", tc.body)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("%s status %d, want 500 body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestConfigInit_ReadFailures covers the startup read-error branch in every
// InitXFromStore: a BoltDB read failure must abort startup with an error, not be
// swallowed into a silent default.
func TestConfigInit_ReadFailures(t *testing.T) {
	setupHandlerStore(t)
	ctrl := middleware.NewRateLimitController(middleware.RateLimitConfig{})
	SetRateLimitController(ctrl, middleware.RateLimitConfig{})

	restore := storage.SetConfigStoreFaultForTest(errInjectedStoreFault)
	defer restore()

	inits := []struct {
		name string
		fn   func() error
	}{
		{"trustedproxy", func() error { _, err := InitTrustedProxyFromStore(); return err }},
		{"accesslog", func() error { _, err := InitAccessLogFromStore(); return err }},
		{"retention", func() error { _, err := InitRequestRetentionFromStore(); return err }},
		{"sync", func() error { _, err := InitSyncWritesFromStore(); return err }},
		{"ratelimit", func() error { _, err := InitRateLimitFromStore(); return err }},
	}
	for _, tc := range inits {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatalf("%s: want error on store read fault, got nil", tc.name)
			}
		})
	}
}
