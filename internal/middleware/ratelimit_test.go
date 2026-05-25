package middleware

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/gin-gonic/gin"
)

// newReq builds a synthetic request with a fixed RemoteAddr and an optional
// X-Forwarded-For header so the IP-resolution tests can drive clientIP without
// a real socket.
func newReq(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// TestClientIPProxyResolution is the security-critical table: it pins exactly
// which X-Forwarded-For entry is trusted for a given proxy count. A regression
// here is a rate-limit-evasion bug, so the cases enumerate the spoofing shapes
// explicitly.
func TestClientIPProxyResolution(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		proxies    int
		want       string
	}{
		{
			// No proxies: XFF must be ignored entirely. A client that sets
			// it is spoofing; the socket peer is the only truth.
			name:       "zero proxies ignores spoofed xff",
			remoteAddr: "203.0.113.5:44321",
			xff:        "1.2.3.4",
			proxies:    0,
			want:       "203.0.113.5",
		},
		{
			// Single proxy: it appended the address it received from (the real
			// client) and sits itself only in RemoteAddr. With one trusted hop
			// the client is the rightmost XFF entry.
			name:       "one proxy picks the client it appended",
			remoteAddr: "10.0.0.1:5000", // the proxy
			xff:        "198.51.100.7",  // proxy appended the genuine client
			proxies:    1,
			want:       "198.51.100.7",
		},
		{
			// Attacker prepends a spoofed entry; the proxy still appends the
			// genuine client to its right. Counting one hop from the right lands
			// on the client, never the leftmost spoof.
			name:       "one proxy ignores prepended spoof",
			remoteAddr: "10.0.0.1:5000",
			xff:        "9.9.9.9, 198.51.100.7", // spoof, then real client
			proxies:    1,
			want:       "198.51.100.7",
		},
		{
			// Three hops: client -> p1 -> p2 -> p3 -> server. p3 (nearest) is in
			// RemoteAddr; XFF holds [client, p1, p2]. The client is parts[len-3].
			name:       "three proxies picks client past inner hops",
			remoteAddr: "10.0.0.3:5000",                   // p3, nearest
			xff:        "198.51.100.9, 10.0.0.1, 10.0.0.2", // client, p1, p2
			proxies:    3,
			want:       "198.51.100.9",
		},
		{
			// Same three-hop chain with a prepended spoof: the extra left entry
			// grows the list without shifting parts[len-3] off the client.
			name:       "three proxies ignores prepended spoof",
			remoteAddr: "10.0.0.3:5000",
			xff:        "1.1.1.1, 198.51.100.9, 10.0.0.1, 10.0.0.2",
			proxies:    3,
			want:       "198.51.100.9",
		},
		{
			// Fewer entries than the configured chain length means the request
			// did not traverse the expected proxies; trust nothing in XFF.
			name:       "short xff falls back to remote addr",
			remoteAddr: "203.0.113.10:5000",
			xff:        "10.0.0.1",
			proxies:    3,
			want:       "203.0.113.10",
		},
		{
			name:       "missing xff falls back to remote addr",
			remoteAddr: "203.0.113.11:5000",
			xff:        "",
			proxies:    2,
			want:       "203.0.113.11",
		},
		{
			// The selected entry is garbage, not an IP. Fall back rather than
			// keying the limiter on attacker-chosen junk.
			name:       "garbage selected entry falls back",
			remoteAddr: "203.0.113.12:5000",
			xff:        "not-an-ip",
			proxies:    1,
			want:       "203.0.113.12",
		},
		{
			name:       "ipv6 client behind one proxy",
			remoteAddr: "[fe80::1]:5000", // the proxy
			xff:        "2001:db8::1",    // proxy appended the genuine client
			proxies:    1,
			want:       "2001:db8::1",
		},
		{
			name:       "ipv6 remote addr fallback",
			remoteAddr: "[2001:db8::99]:5000",
			xff:        "",
			proxies:    1,
			want:       "2001:db8::99",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientIP(newReq(tc.remoteAddr, tc.xff), tc.proxies)
			if got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientIPRemoteAddrNoPort covers the synthetic-request edge where
// RemoteAddr carries no port. The raw value must pass through rather than
// erroring, since the result is only ever a limiter key.
func TestClientIPRemoteAddrNoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "203.0.113.50"
	if got := clientIP(r, 0); got != "203.0.113.50" {
		t.Fatalf("clientIP = %q, want 203.0.113.50", got)
	}
}

// TestRateLimiterBurstThenThrottle exercises the token bucket directly: burst
// requests pass, the next is denied, and after the refill window one more
// passes. Uses a generous refill so the timing is not flaky on slow CI.
func TestRateLimiterBurstThenThrottle(t *testing.T) {
	rl := newRateLimiter(100, 3) // 100 rps refill, depth 3
	t.Cleanup(rl.close)

	const key = "198.51.100.1"
	for i := 0; i < 3; i++ {
		if !rl.allow(key) {
			t.Fatalf("request %d within burst was throttled", i+1)
		}
	}
	if rl.allow(key) {
		t.Fatal("request beyond burst was admitted; token bucket not enforcing depth")
	}

	// At 100 rps a token refills in ~10ms; wait comfortably past that.
	time.Sleep(30 * time.Millisecond)
	if !rl.allow(key) {
		t.Fatal("request after refill window was throttled; bucket not refilling")
	}
}

// TestRateLimiterPerKeyIsolation confirms one client's exhausted bucket does
// not throttle another client. Without per-key isolation a single noisy IP
// would deny service to everyone.
func TestRateLimiterPerKeyIsolation(t *testing.T) {
	rl := newRateLimiter(1, 1)
	t.Cleanup(rl.close)

	if !rl.allow("10.0.0.1") {
		t.Fatal("first client first request throttled")
	}
	if rl.allow("10.0.0.1") {
		t.Fatal("first client second request should be throttled")
	}
	if !rl.allow("10.0.0.2") {
		t.Fatal("second client must have its own fresh bucket")
	}
}

// TestRateLimiterBoundedEviction is the Power-of-10 guard: the map must never
// exceed maxLimiterEntries even when fed far more distinct keys. We cannot
// realistically push 65536 entries in a unit test cheaply, so we assert the
// invariant via evictOldestLocked on a small synthetic map plus a cap check.
func TestRateLimiterBoundedEviction(t *testing.T) {
	rl := newRateLimiter(1000, 1000)
	t.Cleanup(rl.close)

	// Drive well past a small synthetic cap by reusing the eviction path
	// directly: fill the map and confirm eviction keeps it from growing.
	// We stub the cap behaviour by inserting then forcing eviction.
	for i := 0; i < 100; i++ {
		rl.allow(fmt.Sprintf("key-%d", i))
	}
	rl.mu.Lock()
	size := len(rl.entries)
	rl.mu.Unlock()
	if size != 100 {
		t.Fatalf("expected 100 entries, got %d", size)
	}

	// Eviction must remove the least-recently-seen key. Touch key-50 so it is
	// the freshest, then evict and confirm it survives while the true oldest
	// is gone.
	rl.allow("key-50")
	rl.mu.Lock()
	rl.evictOldestLocked()
	_, key0Present := rl.entries["key-0"]
	_, key50Present := rl.entries["key-50"]
	after := len(rl.entries)
	rl.mu.Unlock()

	if key0Present {
		t.Fatal("evictOldestLocked did not remove the least-recently-seen entry")
	}
	if !key50Present {
		t.Fatal("evictOldestLocked removed a recently-seen entry")
	}
	if after != 99 {
		t.Fatalf("after one eviction expected 99 entries, got %d", after)
	}
}

// TestRateLimiterCapEnforced proves the hard cap: insert maxLimiterEntries+N
// distinct keys and confirm the map never exceeds the cap. Runs the real
// allow path so the cap check in allow is what is under test.
func TestRateLimiterCapEnforced(t *testing.T) {
	if testing.Short() {
		t.Skip("cap test inserts many keys; skipped under -short")
	}
	rl := newRateLimiter(1000, 1000)
	t.Cleanup(rl.close)

	total := maxLimiterEntries + 500
	for i := 0; i < total; i++ {
		rl.allow(strconv.Itoa(i))
		rl.mu.Lock()
		size := len(rl.entries)
		rl.mu.Unlock()
		if size > maxLimiterEntries {
			t.Fatalf("map grew to %d, exceeding cap %d", size, maxLimiterEntries)
		}
	}
}

// TestRateLimiterReapIdle confirms the janitor's reap logic removes entries
// older than the TTL while keeping fresh ones. Drives reapIdle directly to
// avoid waiting a full gcInterval.
func TestRateLimiterReapIdle(t *testing.T) {
	rl := newRateLimiter(10, 10)
	t.Cleanup(rl.close)

	rl.mu.Lock()
	rl.entries["stale"] = &limiterEntry{
		limiter:  rate.NewLimiter(rl.rps, rl.burst),
		lastSeen: time.Now().Add(-2 * limiterTTL),
	}
	rl.entries["fresh"] = &limiterEntry{
		limiter:  rate.NewLimiter(rl.rps, rl.burst),
		lastSeen: time.Now(),
	}
	rl.mu.Unlock()

	rl.reapIdle()

	rl.mu.Lock()
	_, stalePresent := rl.entries["stale"]
	_, freshPresent := rl.entries["fresh"]
	rl.mu.Unlock()

	if stalePresent {
		t.Fatal("reapIdle did not remove a stale entry")
	}
	if !freshPresent {
		t.Fatal("reapIdle removed a fresh entry")
	}
}

// TestRateLimiterConcurrentRaceFree hammers allow from many goroutines on a
// mix of keys. Run with -race; the assertion is simply that it does not panic
// or deadlock and the map stays bounded.
func TestRateLimiterConcurrentRaceFree(t *testing.T) {
	rl := newRateLimiter(1000, 1000)
	t.Cleanup(rl.close)

	const workers = 32
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				key := strconv.Itoa((w*iters + i) % 64) // bounded keyspace
				rl.allow(key)
			}
		}(w)
	}
	wg.Wait()

	rl.mu.Lock()
	size := len(rl.entries)
	rl.mu.Unlock()
	if size > maxLimiterEntries {
		t.Fatalf("map grew to %d under concurrency, exceeding cap", size)
	}
}

// buildRateLimitedRouter composes a Gin engine with the rate-limit middleware
// in front of a trivial 200 handler, mirroring how the routers install it.
func buildRateLimitedRouter(cfg RateLimitConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	if cfg.Enabled {
		r.Use(RateLimit(cfg))
	}
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// TestRateLimitMiddlewareThrottles drives the full middleware: a burst beyond
// depth produces 503 SlowDown with Retry-After, and requests under the limit
// return 200.
func TestRateLimitMiddlewareThrottles(t *testing.T) {
	cfg := RateLimitConfig{Enabled: true, RPS: 1, Burst: 2, TrustedProxies: 0}
	r := buildRateLimitedRouter(cfg)

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.77:40000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// Burst of 2 must pass.
	for i := 0; i < 2; i++ {
		if w := send(); w.Code != http.StatusOK {
			t.Fatalf("burst request %d: status %d, want 200", i+1, w.Code)
		}
	}
	// Third immediately after must be throttled.
	w := send()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-limit status %d, want 503", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("throttled response missing Retry-After header")
	}
	// S3 surface (no /api prefix, no JSON Accept) must get XML with SlowDown.
	var body s3ErrorBody
	if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("throttle body not valid XML: %v; body=%q", err, w.Body.String())
	}
	if body.Code != "SlowDown" {
		t.Fatalf("throttle code = %q, want SlowDown", body.Code)
	}
}

// TestRateLimitMiddlewareJSONSurface confirms the admin surface gets a JSON
// SlowDown body so the SPA can render it without an XML parser.
func TestRateLimitMiddlewareJSONSurface(t *testing.T) {
	cfg := RateLimitConfig{Enabled: true, RPS: 1, Burst: 1}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(RateLimit(cfg))
	r.GET("/api/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = "203.0.113.88:40000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := send(); w.Code != http.StatusOK {
		t.Fatalf("first request status %d, want 200", w.Code)
	}
	w := send()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-limit status %d, want 503", w.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("throttle body not valid JSON: %v; body=%q", err, w.Body.String())
	}
	if body.Code != "SlowDown" {
		t.Fatalf("throttle code = %q, want SlowDown", body.Code)
	}
}

// TestRateLimitMiddlewareSpoofedXFFCannotEvade is the integration form of the
// evasion guard: with 0 trusted proxies, rotating a spoofed X-Forwarded-For
// must NOT mint fresh buckets — every request keys on the same socket peer and
// the limiter still trips.
func TestRateLimitMiddlewareSpoofedXFFCannotEvade(t *testing.T) {
	cfg := RateLimitConfig{Enabled: true, RPS: 1, Burst: 1, TrustedProxies: 0}
	r := buildRateLimitedRouter(cfg)

	send := func(spoof string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.99:40000"
		req.Header.Set("X-Forwarded-For", spoof)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	if code := send("1.1.1.1"); code != http.StatusOK {
		t.Fatalf("first request status %d, want 200", code)
	}
	// Rotate the spoofed header; with no trusted proxies it is ignored, so the
	// same real IP is rate-limited regardless.
	if code := send("2.2.2.2"); code != http.StatusServiceUnavailable {
		t.Fatalf("rotated-spoof request status %d, want 503 (evasion not prevented)", code)
	}
}

// buildControllerRouter mounts a controller's middleware in front of a trivial
// 200 handler, exercising the same wiring the production routers install.
func buildControllerRouter(rc *RateLimitController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(rc.Middleware())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// sendFrom drives one request from a fixed client IP and returns the status.
func sendFrom(r *gin.Engine, ip string) int {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestControllerRuntimeEnable proves limiting can be switched on without
// re-mounting the middleware: a disabled controller admits everything, and
// after Apply with an enabled config the same handler throttles.
func TestControllerRuntimeEnable(t *testing.T) {
	rc := NewRateLimitController(RateLimitConfig{Enabled: false})
	t.Cleanup(rc.rl.close)
	r := buildControllerRouter(rc)

	for i := 0; i < 5; i++ {
		if code := sendFrom(r, "198.51.100.1"); code != http.StatusOK {
			t.Fatalf("disabled controller throttled request %d (status %d)", i+1, code)
		}
	}

	rc.Apply(RateLimitConfig{Enabled: true, RPS: 1, Burst: 1})
	if code := sendFrom(r, "198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first request after enable status %d, want 200", code)
	}
	if code := sendFrom(r, "198.51.100.1"); code != http.StatusServiceUnavailable {
		t.Fatalf("over-limit after enable status %d, want 503", code)
	}
}

// TestControllerLiveReconfigureFlushes proves Apply flushes existing buckets so
// a raised burst takes effect immediately for an already-seen client, not just
// for IPs first seen after the swap.
func TestControllerLiveReconfigureFlushes(t *testing.T) {
	rc := NewRateLimitController(RateLimitConfig{Enabled: true, RPS: 1, Burst: 1})
	t.Cleanup(rc.rl.close)
	r := buildControllerRouter(rc)

	if code := sendFrom(r, "198.51.100.2"); code != http.StatusOK {
		t.Fatalf("first request status %d, want 200", code)
	}
	if code := sendFrom(r, "198.51.100.2"); code != http.StatusServiceUnavailable {
		t.Fatalf("expected throttle before reconfigure, got %d", code)
	}
	rc.Apply(RateLimitConfig{Enabled: true, RPS: 1, Burst: 5})
	if code := sendFrom(r, "198.51.100.2"); code != http.StatusOK {
		t.Fatalf("request after reconfigure status %d, want 200 (flush did not apply)", code)
	}
}

// TestControllerRuntimeDisable proves an enabled limiter can be turned off at
// runtime, after which it admits without bound.
func TestControllerRuntimeDisable(t *testing.T) {
	rc := NewRateLimitController(RateLimitConfig{Enabled: true, RPS: 1, Burst: 1})
	t.Cleanup(rc.rl.close)
	r := buildControllerRouter(rc)

	_ = sendFrom(r, "198.51.100.3")
	if code := sendFrom(r, "198.51.100.3"); code != http.StatusServiceUnavailable {
		t.Fatalf("expected throttle while enabled, got %d", code)
	}
	rc.Apply(RateLimitConfig{Enabled: false})
	for i := 0; i < 5; i++ {
		if code := sendFrom(r, "198.51.100.3"); code != http.StatusOK {
			t.Fatalf("disabled controller throttled request %d (status %d)", i+1, code)
		}
	}
}

// TestControllerCurrent confirms Current reflects the seeded then applied config.
func TestControllerCurrent(t *testing.T) {
	rc := NewRateLimitController(RateLimitConfig{Enabled: false, RPS: 2, Burst: 3, TrustedProxies: 1})
	t.Cleanup(rc.rl.close)
	if got := rc.Current(); got.RPS != 2 || got.Burst != 3 || got.TrustedProxies != 1 || got.Enabled {
		t.Fatalf("Current() = %+v, want seeded env config", got)
	}
	want := RateLimitConfig{Enabled: true, RPS: 9, Burst: 4, TrustedProxies: 2}
	rc.Apply(want)
	if got := rc.Current(); got != want {
		t.Fatalf("Current() after Apply = %+v, want %+v", got, want)
	}
}

// TestRateLimitMiddlewareProxySpoofCannotEvade is the evasion guard for the
// behind-a-proxy deployment (TrustedProxies > 0). The trusted proxy always
// appends the genuine client to the right; the attacker controls only the
// prepended prefix and rotates it each request. Both requests must key on the
// same client, so the second trips the limiter. This pins the XFF index: a
// regression that selected the leftmost (spoofed) entry would mint a fresh
// bucket per request and this test would see a 200 instead of a 503.
func TestRateLimitMiddlewareProxySpoofCannotEvade(t *testing.T) {
	cfg := RateLimitConfig{Enabled: true, RPS: 1, Burst: 1, TrustedProxies: 1}
	r := buildRateLimitedRouter(cfg)

	send := func(spoofPrefix string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.1:5000" // the trusted proxy
		req.Header.Set("X-Forwarded-For", spoofPrefix+", 198.51.100.7")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	if code := send("9.9.9.9"); code != http.StatusOK {
		t.Fatalf("first request status %d, want 200", code)
	}
	if code := send("8.8.8.8"); code != http.StatusServiceUnavailable {
		t.Fatalf("rotated-prefix request status %d, want 503 (evasion not prevented)", code)
	}
}

// TestRateLimitDisabledNoThrottle confirms the default (disabled) posture: a
// router that does not install the middleware never throttles, no matter the
// request volume.
func TestRateLimitDisabledNoThrottle(t *testing.T) {
	cfg := RateLimitConfig{Enabled: false}
	r := buildRateLimitedRouter(cfg)
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.1:40000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d throttled while disabled: status %d", i+1, w.Code)
		}
	}
}
