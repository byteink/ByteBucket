package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ByteBucket/internal/storage"
)

func reqWith(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// TestResolveClientIP is the security-critical table for header-based client-IP
// resolution. It pins which header and which entry are trusted, and proves a
// spoofed or unconfigured header can never override the socket peer.
func TestResolveClientIP(t *testing.T) {
	cases := []struct {
		name        string
		remoteAddr  string
		headers     map[string]string
		trusted     []string
		useLeftmost bool
		want        string
	}{
		{
			// Nothing trusted: a client-set X-Forwarded-For is spoofing; the
			// socket peer is the only truth.
			name:       "no trusted headers ignores spoofed xff",
			remoteAddr: "203.0.113.5:44321",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4"},
			trusted:    nil,
			want:       "203.0.113.5",
		},
		{
			// Cloudflare writes the real client into CF-Connecting-IP; trusting
			// it returns the client even though XFF carries a prepended spoof.
			name:       "cloudflare cf-connecting-ip",
			remoteAddr: "10.0.0.1:5000",
			headers:    map[string]string{"CF-Connecting-IP": "198.51.100.7", "X-Forwarded-For": "9.9.9.9, 198.51.100.7"},
			trusted:    []string{"CF-Connecting-IP"},
			want:       "198.51.100.7",
		},
		{
			// Single proxy appended the client to the right; the rightmost entry
			// is the address it wrote and the attacker's prepended spoof is left.
			name:       "xff rightmost is the nearest trusted hop",
			remoteAddr: "10.0.0.1:5000",
			headers:    map[string]string{"X-Forwarded-For": "9.9.9.9, 198.51.100.7"},
			trusted:    []string{"X-Forwarded-For"},
			want:       "198.51.100.7",
		},
		{
			// Leftmost scan finds no valid IP → fall back to the socket peer.
			name:        "leftmost all garbage falls back",
			remoteAddr:  "203.0.113.20:5000",
			headers:     map[string]string{"X-Forwarded-For": "junk, also-junk"},
			trusted:     []string{"X-Forwarded-For"},
			useLeftmost: true,
			want:        "203.0.113.20",
		},
		{
			name:        "xff leftmost trusts the first entry",
			remoteAddr:  "10.0.0.1:5000",
			headers:     map[string]string{"X-Forwarded-For": "198.51.100.7, 10.0.0.2"},
			trusted:     []string{"X-Forwarded-For"},
			useLeftmost: true,
			want:        "198.51.100.7",
		},
		{
			// Header list is a priority order: the first present header wins.
			name:       "header priority order picks first present",
			remoteAddr: "10.0.0.1:5000",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9"},
			trusted:    []string{"CF-Connecting-IP", "X-Forwarded-For"},
			want:       "203.0.113.9",
		},
		{
			name:       "missing configured header falls back to remote addr",
			remoteAddr: "203.0.113.11:5000",
			headers:    nil,
			trusted:    []string{"CF-Connecting-IP"},
			want:       "203.0.113.11",
		},
		{
			// A header carrying junk must fall back, never key on attacker junk.
			name:       "garbage header value falls back to remote addr",
			remoteAddr: "203.0.113.12:5000",
			headers:    map[string]string{"CF-Connecting-IP": "not-an-ip"},
			trusted:    []string{"CF-Connecting-IP"},
			want:       "203.0.113.12",
		},
		{
			// Rightmost scan skips trailing garbage to the last valid address.
			name:       "rightmost skips trailing garbage to last valid ip",
			remoteAddr: "10.0.0.1:5000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7, garbage"},
			trusted:    []string{"X-Forwarded-For"},
			want:       "198.51.100.7",
		},
		{
			name:       "ipv6 client via header",
			remoteAddr: "[fe80::1]:5000",
			headers:    map[string]string{"CF-Connecting-IP": "2001:db8::1"},
			trusted:    []string{"CF-Connecting-IP"},
			want:       "2001:db8::1",
		},
		{
			name:       "ipv6 remote addr fallback",
			remoteAddr: "[2001:db8::99]:5000",
			headers:    nil,
			trusted:    []string{"X-Forwarded-For"},
			want:       "2001:db8::99",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveClientIP(reqWith(tc.remoteAddr, tc.headers), tc.trusted, tc.useLeftmost)
			if got != tc.want {
				t.Fatalf("resolveClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveClientIPReadsLiveConfig proves the exported entry point honours the
// shared storage config rather than a hardcoded header.
func TestResolveClientIPReadsLiveConfig(t *testing.T) {
	storage.SetTrustedProxy(storage.TrustedProxyConfig{Headers: []string{"CF-Connecting-IP"}})
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })
	r := reqWith("10.0.0.1:5000", map[string]string{"CF-Connecting-IP": "9.9.9.9"})
	if got := ResolveClientIP(r); got != "9.9.9.9" {
		t.Fatalf("ResolveClientIP = %q, want 9.9.9.9", got)
	}
}

// TestDetectProxyHeader pins the configured-first, then vendor-fallback order,
// and the empty result when no proxy header is present.
func TestDetectProxyHeader(t *testing.T) {
	// Configured header absent; a known vendor header present → fallback hit.
	r := reqWith("10.0.0.1:5000", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	if got := DetectProxyHeader(r, []string{"CF-Connecting-IP"}); got != "X-Forwarded-For" {
		t.Fatalf("DetectProxyHeader = %q, want X-Forwarded-For (fallback)", got)
	}
	// Configured header present takes precedence over a vendor fallback.
	r2 := reqWith("10.0.0.1:5000", map[string]string{"CF-Connecting-IP": "1.2.3.4", "X-Forwarded-For": "5.6.7.8"})
	if got := DetectProxyHeader(r2, []string{"CF-Connecting-IP"}); got != "CF-Connecting-IP" {
		t.Fatalf("DetectProxyHeader = %q, want CF-Connecting-IP", got)
	}
	// No proxy header at all → empty (looks directly exposed).
	if got := DetectProxyHeader(reqWith("10.0.0.1:5000", nil), nil); got != "" {
		t.Fatalf("DetectProxyHeader = %q, want empty", got)
	}
}

// TestRemoteIP covers the bare-peer helper, including the no-port synthetic edge.
func TestRemoteIP(t *testing.T) {
	if got := RemoteIP(reqWith("203.0.113.50:1234", nil)); got != "203.0.113.50" {
		t.Fatalf("RemoteIP = %q, want 203.0.113.50", got)
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "203.0.113.51"
	if got := RemoteIP(r); got != "203.0.113.51" {
		t.Fatalf("RemoteIP no-port = %q, want 203.0.113.51", got)
	}
}
