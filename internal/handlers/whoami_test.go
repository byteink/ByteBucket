package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// TestWhoAmIHandler proves the validation endpoint resolves the IP via the live
// trusted-proxy config and surfaces the raw signals an operator needs: with
// CF-Connecting-IP trusted, the resolved IP is the header value and the detected
// header matches; with nothing trusted, it falls back to the socket peer.
func TestWhoAmIHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })

	r := gin.New()
	r.GET("/whoami", GetWhoAmIHandler)

	call := func(headers map[string]string) whoAmIDTO {
		req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
		req.RemoteAddr = "10.0.0.1:5000"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		var out whoAmIDTO
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	storage.SetTrustedProxy(storage.TrustedProxyConfig{Headers: []string{"CF-Connecting-IP"}})
	got := call(map[string]string{"CF-Connecting-IP": "9.9.9.9", "X-Forwarded-For": "1.2.3.4"})
	if got.IP != "9.9.9.9" {
		t.Fatalf("ip = %q, want 9.9.9.9", got.IP)
	}
	if got.RemoteAddr != "10.0.0.1" {
		t.Fatalf("remoteAddr = %q, want 10.0.0.1", got.RemoteAddr)
	}
	if got.DetectedHeader != "CF-Connecting-IP" {
		t.Fatalf("detectedHeader = %q, want CF-Connecting-IP", got.DetectedHeader)
	}
	if got.ForwardedFor != "1.2.3.4" {
		t.Fatalf("forwardedFor = %q, want 1.2.3.4", got.ForwardedFor)
	}

	storage.SetTrustedProxy(storage.TrustedProxyConfig{})
	got = call(map[string]string{"X-Forwarded-For": "1.2.3.4"})
	if got.IP != "10.0.0.1" {
		t.Fatalf("ip = %q, want socket peer 10.0.0.1 when nothing trusted", got.IP)
	}
	if len(got.TrustedHeaders) != 0 {
		t.Fatalf("trustedHeaders = %v, want empty", got.TrustedHeaders)
	}
}
