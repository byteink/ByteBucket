package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

func TestSanitizeTrustedProxyHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"empty is valid", []string{}, []string{}, true},
		{"single vendor header", []string{"CF-Connecting-IP"}, []string{"CF-Connecting-IP"}, true},
		{"trims surrounding space", []string{"  X-Forwarded-For  "}, []string{"X-Forwarded-For"}, true},
		{"dedups case-insensitively", []string{"X-Forwarded-For", "x-forwarded-for"}, []string{"X-Forwarded-For"}, true},
		{"rejects crlf injection", []string{"X-Forwarded-For\r\nEvil: 1"}, nil, false},
		{"rejects spaces inside", []string{"X Forwarded For"}, nil, false},
		{"rejects path separator", []string{"../etc"}, nil, false},
		{"rejects empty token", []string{""}, nil, false},
		{"rejects over-long token", []string{string(make([]byte, 65))}, nil, false},
		{"rejects too many headers", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sanitizeTrustedProxyHeaders(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && !equalStrings(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTrustedProxyEndpoints exercises the full GET/PUT cycle against real
// persistence: a valid PUT applies live and persists, GET reflects it,
// init-from-store reloads it after a fresh process, and a hostile header name is
// rejected without mutating the live config.
func TestTrustedProxyEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	storage.SetTrustedProxy(storage.TrustedProxyConfig{})
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })

	r := gin.New()
	r.GET("/tp", GetTrustedProxyHandler)
	r.PUT("/tp", PutTrustedProxyHandler)

	// Default: empty headers, rightmost.
	w := doReq(r, http.MethodGet, "/tp", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	var got trustedProxyDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Headers) != 0 || got.UseLeftmostIP {
		t.Fatalf("default = %+v, want empty rightmost", got)
	}

	// Valid PUT persists and applies live.
	w = doReq(r, http.MethodPut, "/tp", `{"headers":["CF-Connecting-IP"],"useLeftmostIP":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status %d body=%s", w.Code, w.Body.String())
	}
	live := storage.TrustedProxy()
	if len(live.Headers) != 1 || live.Headers[0] != "CF-Connecting-IP" || !live.UseLeftmostIP {
		t.Fatalf("live config not applied: %+v", live)
	}

	// A fresh process: reset live state, then init-from-store reloads it.
	storage.SetTrustedProxy(storage.TrustedProxyConfig{})
	if _, err := InitTrustedProxyFromStore(); err != nil {
		t.Fatalf("InitTrustedProxyFromStore: %v", err)
	}
	if reloaded := storage.TrustedProxy(); len(reloaded.Headers) != 1 || reloaded.Headers[0] != "CF-Connecting-IP" {
		t.Fatalf("persisted config not reloaded: %+v", reloaded)
	}

	// A malformed JSON body is rejected before any validation runs.
	w = doReq(r, http.MethodPut, "/tp", `not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed-body PUT status %d, want 400", w.Code)
	}

	// Hostile header name is rejected and must not mutate the live config.
	w = doReq(r, http.MethodPut, "/tp", `{"headers":["X-Forwarded-For\r\nEvil: 1"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("hostile PUT status %d, want 400", w.Code)
	}
	if after := storage.TrustedProxy(); len(after.Headers) != 1 || after.Headers[0] != "CF-Connecting-IP" {
		t.Fatalf("live config mutated by rejected PUT: %+v", after)
	}
}

// TestInitTrustedProxyIgnoresInvalidPersisted proves a structurally-valid but
// content-invalid persisted header list (e.g. written by an older build) is not
// applied at startup, so a bad stored value cannot reach the header lookup.
func TestInitTrustedProxyIgnoresInvalidPersisted(t *testing.T) {
	setupHandlerStore(t)
	storage.SetTrustedProxy(storage.TrustedProxyConfig{})
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })

	if err := storage.PutConfigValue(trustedProxyHeadersKey, []byte(`["bad header"]`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := InitTrustedProxyFromStore(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := storage.TrustedProxy(); len(got.Headers) != 0 {
		t.Fatalf("invalid persisted headers applied: %+v", got)
	}
}
