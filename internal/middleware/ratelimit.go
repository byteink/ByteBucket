package middleware

import (
	"encoding/json"
	"encoding/xml"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/gin-gonic/gin"
)

// slowDownMessage is the human-readable text paired with the SlowDown code.
// AWS SDKs pattern-match the Code element (and auto-retry SlowDown with
// backoff); the message is for operators reading logs or the admin UI.
const slowDownMessage = "Reduce your request rate"

// RateLimitConfig captures the operator-tunable knobs for request rate
// limiting. The environment seeds a baseline at startup (see cmd/ByteBucket);
// an admin may then override it at runtime via the admin API, and the override
// wins. RateLimitController owns the live value behind an atomic pointer, so a
// config swap is visible to in-flight requests without locking and without a
// restart. Pass copies, never share a pointer to a mutated value.
//
// The feature is opt-in: a request only pays the limiter cost when the live
// config has Enabled true. When disabled the middleware is still mounted but
// short-circuits after a single atomic load, so the disabled path stays cheap.
type RateLimitConfig struct {
	Enabled bool
	// RPS is the sustained token refill rate per client IP, in tokens per
	// second. RPS <= 0 with Enabled true is treated as a misconfiguration by
	// newRateLimiter, which clamps to a safe positive floor rather than
	// dividing by zero or admitting an unbounded stream.
	RPS float64
	// Burst is the token-bucket depth: the largest instantaneous spike a
	// single client may make before the sustained RPS rate gates them.
	Burst int
	// TrustedProxies is the number of reverse-proxy hops in front of this
	// server. It selects which X-Forwarded-For entry is the real client; see
	// clientIP for the (N+1)-th-from-the-right rationale.
	TrustedProxies int
}

// Rate-limit store bounds. These are constants, not config, because the
// Power-of-10 rule forbids unbounded resource allocation after startup: an
// attacker who can mint arbitrary source IPs (trivially, by spoofing the
// X-Forwarded-For entries we do NOT trust, or genuinely from a botnet) would
// otherwise grow the limiter map without limit and exhaust memory — turning a
// DoS *defence* into a DoS *vector*.
//
//   - maxLimiterEntries caps the live map. Once full, the least-recently-seen
//     entry is evicted to admit a new client. An evicted attacker simply gets
//     a fresh full bucket, which is acceptable: the cap protects memory, the
//     per-IP limiter protects throughput, and a single IP cannot both stay hot
//     enough to keep its entry and exceed its own rate.
//   - limiterTTL is how long an idle entry survives before the janitor reaps
//     it, reclaiming memory for IPs that have gone quiet.
//   - gcInterval is how often the janitor sweeps. Kept coarse so the lock is
//     held briefly and infrequently relative to request traffic.
const (
	maxLimiterEntries = 65536
	limiterTTL        = 10 * time.Minute
	gcInterval        = time.Minute
)

// slowDownRetryAfterSeconds is the Retry-After hint emitted with a throttle
// response. One second matches the smallest meaningful backoff for a
// per-second token bucket; AWS SDKs treat SlowDown as retryable and apply
// their own exponential backoff on top, so a conservative floor is correct.
const slowDownRetryAfterSeconds = 1

// limiterEntry pairs a token bucket with its last-seen timestamp so the
// janitor can evict idle clients and the admit path can refresh recency for
// LRU eviction under the size cap.
type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rateLimiter is a bounded, concurrency-safe store of per-key token buckets.
// All access is guarded by mu; the map never exceeds maxLimiterEntries
// entries, and a background janitor evicts entries idle longer than
// limiterTTL. The bound is the whole point — see the const block above.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	rps     rate.Limit
	burst   int

	stop chan struct{}
}

// newRateLimiter builds a store and starts its janitor goroutine. rps is
// clamped to a positive floor so an operator who enables limiting with a
// zero/negative RPS does not create a limiter that admits nothing (burst-only)
// or panics; burst is clamped to at least 1 for the same reason.
func newRateLimiter(rps float64, burst int) *rateLimiter {
	if rps <= 0 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	rl := &rateLimiter{
		entries: make(map[string]*limiterEntry, 1024),
		rps:     rate.Limit(rps),
		burst:   burst,
		stop:    make(chan struct{}),
	}
	go rl.janitor()
	return rl
}

// reconfigure swaps the rate/burst applied to new buckets and flushes the live
// map so the change takes effect uniformly and immediately for every client,
// not just IPs first seen after the swap. The same clamps as newRateLimiter
// apply. Flushing is bounded work (a single map replacement) and runs only on
// the rare admin-driven reconfigure path, so it does not weaken the
// Power-of-10 bound on the live set.
func (rl *rateLimiter) reconfigure(rps float64, burst int) {
	if rps <= 0 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	rl.mu.Lock()
	rl.rps = rate.Limit(rps)
	rl.burst = burst
	rl.entries = make(map[string]*limiterEntry, 1024)
	rl.mu.Unlock()
}

// allow reports whether a request keyed by key may proceed. It consumes one
// token from the key's bucket, creating the bucket on first sighting and
// evicting the least-recently-seen entry first if the map is at capacity.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if e, ok := rl.entries[key]; ok {
		e.lastSeen = now
		return e.limiter.Allow()
	}

	// New key. Enforce the hard cap before inserting so the map size is
	// statically bounded by maxLimiterEntries at all times.
	if len(rl.entries) >= maxLimiterEntries {
		rl.evictOldestLocked()
	}
	lim := rate.NewLimiter(rl.rps, rl.burst)
	rl.entries[key] = &limiterEntry{limiter: lim, lastSeen: now}
	return lim.Allow()
}

// evictOldestLocked removes the single least-recently-seen entry. Caller must
// hold rl.mu. A full linear scan is acceptable: it runs only on the rare path
// where the map is already at the (large) cap, and keeping a separate LRU
// index would add concurrency surface for no real-world benefit at this scale.
func (rl *rateLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for k, e := range rl.entries {
		if first || e.lastSeen.Before(oldest) {
			oldestKey = k
			oldest = e.lastSeen
			first = false
		}
	}
	if !first {
		delete(rl.entries, oldestKey)
	}
}

// janitor periodically reaps entries idle longer than limiterTTL, reclaiming
// memory for clients that have gone quiet. It exits when stop is closed so the
// goroutine does not outlive the limiter in tests.
func (rl *rateLimiter) janitor() {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			rl.reapIdle()
		}
	}
}

// reapIdle deletes every entry not seen within limiterTTL. Bounded by the map
// size, which is itself bounded by maxLimiterEntries.
func (rl *rateLimiter) reapIdle() {
	cutoff := time.Now().Add(-limiterTTL)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, e := range rl.entries {
		if e.lastSeen.Before(cutoff) {
			delete(rl.entries, k)
		}
	}
}

// close stops the janitor goroutine. Used by tests; the production limiter
// lives for the process lifetime, so main never needs to call it.
func (rl *rateLimiter) close() {
	close(rl.stop)
}

// clientIP resolves the real client address, honouring exactly trustedProxies
// reverse-proxy hops in front of this server.
//
// Each proxy in a standard chain (nginx proxy_add_x_forwarded_for, traefik,
// AWS ALB, ...) appends the address of the host it received the request FROM.
// So the proxy directly connected to us records the previous hop in
// X-Forwarded-For and appears itself only in the transport RemoteAddr — it is
// NOT in XFF. That makes the nearest proxy the first of our trustedProxies
// hops, with the remaining (trustedProxies-1) hops being the rightmost XFF
// entries our infrastructure wrote. The genuine client is therefore the entry
// immediately to their left: parts[len-trustedProxies].
//
// Counting from the right past exactly the hops we operate is what defeats the
// classic evasion: an attacker can only PREPEND entries (everything left of
// what our outermost trusted proxy appended), and prepending grows the list
// without moving parts[len-trustedProxies]. Trusting the leftmost entry
// instead (naive XFF[0]) would let a rotated spoof mint a fresh bucket per
// request. This matches how Gin's ClientIP, Express' proxy-addr, and RFC 7239
// resolve the client behind a known number of trusted hops.
//
// If the header has fewer than trustedProxies entries the request did not
// traverse the expected chain; we trust no XFF entry and fall back to
// RemoteAddr, which no client can forge. A selected entry that is not a valid
// IP falls back the same way.
func clientIP(r *http.Request, trustedProxies int) string {
	remoteIP := remoteAddrIP(r)

	if trustedProxies <= 0 {
		// We sit directly on the network; the only trustworthy source is the
		// socket peer. Ignore XFF entirely so a spoofed header cannot key the
		// limiter.
		return remoteIP
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remoteIP
	}

	parts := splitTrim(xff)
	// Fewer entries than hops means the chain was shorter than configured; do
	// not guess — fall back to the socket peer. The nearest proxy is RemoteAddr
	// (not in XFF), so a fully-traversed chain leaves at least trustedProxies
	// entries here.
	if len(parts) < trustedProxies {
		return remoteIP
	}

	candidate := parts[len(parts)-trustedProxies]
	if ip := net.ParseIP(candidate); ip != nil {
		return ip.String()
	}
	return remoteIP
}

// remoteAddrIP extracts the bare IP from r.RemoteAddr ("ip:port"). On the rare
// path where the address carries no port (synthetic requests in tests) the
// raw value is returned. The result is only ever used as a limiter key, so a
// best-effort string is sufficient — it never touches the filesystem or auth.
func remoteAddrIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// splitTrim splits a comma-separated header value and trims surrounding
// whitespace from each element, dropping empties. Bounded by the header size,
// which net/http caps via MaxHeaderBytes.
func splitTrim(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// RateLimitController owns the live rate-limit configuration and the single
// bounded token-bucket store behind it. It is created once at startup from the
// environment baseline and shared by both HTTP surfaces (port 9000 and 9001),
// so a client's combined request stream is throttled against one per-IP budget
// and an admin override applies everywhere at once. The config is held in an
// atomic pointer: Middleware reads it per request without locking, and Apply
// swaps it without a restart.
type RateLimitController struct {
	rl  *rateLimiter
	cfg atomic.Pointer[RateLimitConfig]
}

// NewRateLimitController builds the controller from the startup (environment)
// config. The token-bucket store is created unconditionally — even when the
// config is disabled — so a later runtime Apply can enable limiting without
// re-plumbing the middleware. The store is idle (no buckets, no admits) while
// disabled, so this costs one goroutine and an empty map.
func NewRateLimitController(cfg RateLimitConfig) *RateLimitController {
	rc := &RateLimitController{rl: newRateLimiter(cfg.RPS, cfg.Burst)}
	cp := cfg
	rc.cfg.Store(&cp)
	return rc
}

// Apply swaps the live config and reconfigures the underlying store so the new
// rate/burst take effect immediately for all clients. Safe to call from a
// request handler (the admin API) while other requests are in flight.
func (rc *RateLimitController) Apply(cfg RateLimitConfig) {
	rc.rl.reconfigure(cfg.RPS, cfg.Burst)
	cp := cfg
	rc.cfg.Store(&cp)
}

// Current returns a copy of the live config for the admin API to report.
func (rc *RateLimitController) Current() RateLimitConfig {
	if p := rc.cfg.Load(); p != nil {
		return *p
	}
	return RateLimitConfig{}
}

// Middleware returns the Gin handler that throttles requests per resolved
// client IP. It is mounted EARLY in both chains — after RequestID/Log/Metrics
// (so a throttled request is still observable and carries a correlatable ID)
// but BEFORE auth (so an unauthenticated flood is rejected before it reaches
// the comparatively expensive signature verification and filesystem ACL
// lookups). Throttling the unauthenticated surface is the primary goal; an
// attacker need not hold credentials to flood.
//
// Unlike the previous design the handler is mounted unconditionally; when the
// live config is disabled it returns after a single atomic load, so the
// disabled path stays cheap while still allowing a runtime enable.
func (rc *RateLimitController) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := rc.cfg.Load()
		if cfg == nil || !cfg.Enabled {
			c.Next()
			return
		}
		key := clientIP(c.Request, cfg.TrustedProxies)
		if rc.rl.allow(key) {
			c.Next()
			return
		}
		c.Header("Retry-After", strconv.Itoa(slowDownRetryAfterSeconds))
		writeSlowDown(c)
	}
}

// RateLimit returns a standalone throttling middleware for a static config. It
// is a thin wrapper over a dedicated controller, retained for tests and any
// caller that does not need runtime reconfiguration.
func RateLimit(cfg RateLimitConfig) gin.HandlerFunc {
	return NewRateLimitController(cfg).Middleware()
}

// writeSlowDown emits a protocol-correct throttle response and aborts the
// chain. The status is 503 with the S3 SlowDown code: AWS SDKs classify
// SlowDown as retryable and apply exponential backoff, so an S3 client is
// nudged to slow down rather than failing hard. Admin/JSON callers get the
// same code in JSON.
//
// The shape reuses this package's s3ErrorBody (defined in body_limit.go) and
// pulls the request ID from the x-amz-request-id response header set earlier
// in the chain, exactly as writeEntityTooLarge does, so the limiter stays free
// of a handlers import that would form a cycle.
func writeSlowDown(c *gin.Context) {
	body := s3ErrorBody{
		Code:      "SlowDown",
		Message:   slowDownMessage,
		RequestId: c.Writer.Header().Get(requestIDHeader),
	}
	if prefersJSON(c.Request) {
		c.Header("Content-Type", "application/json")
		c.AbortWithStatus(http.StatusServiceUnavailable)
		_ = json.NewEncoder(c.Writer).Encode(body)
		return
	}
	c.Header("Content-Type", "application/xml")
	c.AbortWithStatus(http.StatusServiceUnavailable)
	_ = xml.NewEncoder(c.Writer).Encode(body)
}
