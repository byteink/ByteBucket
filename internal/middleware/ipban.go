package middleware

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// accessDeniedMessage is the canonical S3 text for AccessDenied. A banned
// client gets exactly what any unauthorised S3 call gets, so the response
// tells a scanner nothing about the ban itself.
const accessDeniedMessage = "Access Denied"

// IPBanConfig captures the operator-tunable knobs for the failed-auth ban on
// the storage surface: an IP that produces MaxFailures 401/403 responses
// within WindowSeconds is refused for BanSeconds. It follows RateLimitConfig:
// the environment seeds a baseline, a persisted admin override wins, and
// IPBanController holds the live value behind an atomic pointer. Durations
// are whole seconds so the wire, env and in-memory shapes match and no
// caller-supplied integer can overflow time.Duration.
type IPBanConfig struct {
	Enabled       bool
	MaxFailures   int
	WindowSeconds int
	BanSeconds    int
}

// Defaults and bounds. The caps reject hostile or fat-fingered values: a
// counting window longer than an hour stops being "a short burst", and a ban
// longer than a week is a blocklist, which belongs in a firewall.
const (
	defaultIPBanMaxFailures   = 20
	defaultIPBanWindowSeconds = 60
	defaultIPBanSeconds       = 15 * 60

	MaxIPBanFailures      = 10_000
	MaxIPBanWindowSeconds = 3600
	MaxIPBanSeconds       = 7 * 24 * 3600
)

// Ban store bounds (Power-of-10: no unbounded allocation driven by input). A
// botnet minting source IPs must not be able to grow either table without
// limit. Each table holds at most maxBanEntries IPs; with both tables full of
// 39-character IPv6 keys the measured heap is about 17 MiB, which is the
// ceiling. When full, the failure table evicts the oldest counting
// window and the ban table the soonest-expiring ban: memory is the hard
// limit, and an evicted attacker merely has to start over.
const (
	maxBanEntries   = 65536
	banGCInterval   = time.Minute
	banMapInitAlloc = 256
)

// cgnatBlock is RFC 6598 shared address space. Carrier-grade NAT puts many
// subscribers behind one of these, and some platforms use it internally, so
// it is treated like RFC 1918 space.
var cgnatBlock = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// DefaultIPBanConfig returns the built-in baseline: off, 20 failures per 60s,
// 15 minute ban.
func DefaultIPBanConfig() IPBanConfig {
	return IPBanConfig{
		MaxFailures:   defaultIPBanMaxFailures,
		WindowSeconds: defaultIPBanWindowSeconds,
		BanSeconds:    defaultIPBanSeconds,
	}
}

func inRange(v, lo, hi int) bool { return v >= lo && v <= hi }

// Validate reports the first out-of-range field. Bounds are checked even when
// disabled so a stored config is always safe to enable later.
func (c IPBanConfig) Validate() error {
	if !inRange(c.MaxFailures, 1, MaxIPBanFailures) {
		return fmt.Errorf("maxFailures must be between 1 and %d", MaxIPBanFailures)
	}
	if !inRange(c.WindowSeconds, 1, MaxIPBanWindowSeconds) {
		return fmt.Errorf("windowSeconds must be between 1 and %d", MaxIPBanWindowSeconds)
	}
	if !inRange(c.BanSeconds, 1, MaxIPBanSeconds) {
		return fmt.Errorf("banSeconds must be between 1 and %d", MaxIPBanSeconds)
	}
	return nil
}

// Normalized replaces each out-of-range field with its default, independently,
// so the store never runs on a value Validate would reject. It is the clamp
// counterpart to Validate, as newRateLimiter is to validateRateLimit.
func (c IPBanConfig) Normalized() IPBanConfig {
	d := DefaultIPBanConfig()
	if !inRange(c.MaxFailures, 1, MaxIPBanFailures) {
		c.MaxFailures = d.MaxFailures
	}
	if !inRange(c.WindowSeconds, 1, MaxIPBanWindowSeconds) {
		c.WindowSeconds = d.WindowSeconds
	}
	if !inRange(c.BanSeconds, 1, MaxIPBanSeconds) {
		c.BanSeconds = d.BanSeconds
	}
	return c
}

// bannableIP returns the canonical key for s and whether it may be banned at
// all. Only public unicast addresses qualify. If trusted-proxy headers are
// misconfigured every request resolves to the reverse proxy's internal
// address; banning that would take the whole service down, so loopback,
// private, link-local, CGNAT and anything unparseable are never banned.
func bannableIP(s string) (string, bool) {
	ip := net.ParseIP(s)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || cgnatBlock.Contains(ip) {
		return "", false
	}
	return ip.String(), true
}

// failEntry counts auth failures inside one fixed window. A fixed window lets
// a client squeeze up to 2*MaxFailures-2 failures across a boundary; that is
// accepted because a sliding log would need per-failure timestamps and
// multiply the memory ceiling by MaxFailures.
type failEntry struct {
	count int
	start time.Time
}

// banStore is the bounded, concurrency-safe pair of tables behind the ban.
// All access is guarded by mu. now is a seam so tests drive expiry with a
// fake clock instead of sleeping.
type banStore struct {
	mu     sync.Mutex
	fails  map[string]*failEntry
	bans   map[string]time.Time
	limit  int
	max    int
	window time.Duration
	ban    time.Duration
	now    func() time.Time
	stop   chan struct{}
}

// newBanStore builds a store capped at limit entries per table and starts its
// janitor. cfg must already be normalized.
func newBanStore(cfg IPBanConfig, limit int) *banStore {
	s := &banStore{limit: limit, now: time.Now, stop: make(chan struct{})}
	s.reconfigure(cfg)
	go s.janitor(banGCInterval)
	return s
}

// reconfigure swaps the thresholds and flushes both tables so the change
// applies uniformly. Flushing also lifts every active ban, which gives the
// operator an "unban all" by saving the settings.
func (s *banStore) reconfigure(cfg IPBanConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.max = cfg.MaxFailures
	s.window = time.Duration(cfg.WindowSeconds) * time.Second
	s.ban = time.Duration(cfg.BanSeconds) * time.Second
	s.fails = make(map[string]*failEntry, banMapInitAlloc)
	s.bans = make(map[string]time.Time, banMapInitAlloc)
}

// banned reports whether ip is serving a ban. Expired bans are dropped lazily
// here so expiry is exact even between janitor sweeps.
func (s *banStore) banned(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bannedLocked(ip, s.now())
}

func (s *banStore) bannedLocked(ip string, now time.Time) bool {
	until, ok := s.bans[ip]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(s.bans, ip)
	return false
}

// fail records one auth failure for ip. It returns the ban expiry, the
// failure count, and true when this failure imposed a ban. A failure from an
// IP already serving a ban (a request that raced past the check) is ignored,
// so nothing can extend a ban once imposed.
func (s *banStore) fail(ip string) (time.Time, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.bannedLocked(ip, now) {
		return time.Time{}, 0, false
	}
	e := s.failEntryLocked(ip, now)
	e.count++
	if e.count < s.max {
		return time.Time{}, e.count, false
	}
	n := e.count
	delete(s.fails, ip)
	if len(s.bans) >= s.limit {
		delete(s.bans, oldestKey(s.bans, func(t time.Time) time.Time { return t }))
	}
	until := now.Add(s.ban)
	s.bans[ip] = until
	return until, n, true
}

// failEntryLocked returns ip's counter, starting a fresh window when there is
// none or the current one has elapsed. Inserting enforces the cap first.
func (s *banStore) failEntryLocked(ip string, now time.Time) *failEntry {
	e, ok := s.fails[ip]
	if ok && now.Sub(e.start) < s.window {
		return e
	}
	if !ok && len(s.fails) >= s.limit {
		delete(s.fails, oldestKey(s.fails, func(f *failEntry) time.Time { return f.start }))
	}
	e = &failEntry{start: now}
	s.fails[ip] = e
	return e
}

// oldestKey returns the key whose timestamp is earliest. A linear scan is
// acceptable: it runs only when a table is already at its cap, mirroring
// rateLimiter.evictOldestLocked. Bounded by the table size.
func oldestKey[V any](m map[string]V, at func(V) time.Time) string {
	var key string
	var oldest time.Time
	first := true
	for k, v := range m {
		if t := at(v); first || t.Before(oldest) {
			key, oldest, first = k, t, false
		}
	}
	return key
}

// reap drops elapsed counting windows and expired bans. Bounded by the table
// sizes, themselves bounded by limit.
func (s *banStore) reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.fails {
		if now.Sub(e.start) >= s.window {
			delete(s.fails, k)
		}
	}
	for k, until := range s.bans {
		if !now.Before(until) {
			delete(s.bans, k)
		}
	}
}

// janitor reaps every interval so IPs that go quiet release their memory. It
// exits when stop is closed so tests do not leak the goroutine.
func (s *banStore) janitor(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.reap()
		}
	}
}

// close stops the janitor. Used by tests; production lives for the process.
func (s *banStore) close() {
	close(s.stop)
}

// IPBanController owns the live ban config and the bounded store behind it,
// mirroring RateLimitController. It is mounted on the storage surface only:
// the admin surface has its own credential lockout.
type IPBanController struct {
	store *banStore
	cfg   atomic.Pointer[IPBanConfig]
}

// NewIPBanController builds the controller from the startup config. The store
// exists even when disabled so a runtime Apply can enable the ban without
// re-plumbing the middleware.
func NewIPBanController(cfg IPBanConfig) *IPBanController {
	n := cfg.Normalized()
	bc := &IPBanController{store: newBanStore(n, maxBanEntries)}
	bc.cfg.Store(&n)
	return bc
}

// Apply swaps the live config and reconfigures (and flushes) the store. Safe
// to call from the admin API while requests are in flight.
func (bc *IPBanController) Apply(cfg IPBanConfig) {
	n := cfg.Normalized()
	bc.store.reconfigure(n)
	bc.cfg.Store(&n)
}

// Current returns a copy of the live config for the admin API to report.
func (bc *IPBanController) Current() IPBanConfig {
	if p := bc.cfg.Load(); p != nil {
		return *p
	}
	return IPBanConfig{}
}

// Middleware refuses banned client IPs before auth and handlers run, then
// counts the request as a failure when its final status is 401 or 403. 404 is
// deliberately not a failure (clients such as imgproxy fetch deleted objects),
// nor are 429/5xx. A refused request returns before c.Next, so it neither
// counts as a failure nor touches the ban's expiry. When disabled the cost is
// one atomic load.
func (bc *IPBanController) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := bc.cfg.Load()
		if cfg == nil || !cfg.Enabled {
			c.Next()
			return
		}
		ip, ok := bannableIP(ResolveClientIP(c.Request))
		if !ok {
			c.Next()
			return
		}
		if bc.store.banned(ip) {
			writeS3Error(c, http.StatusForbidden, "AccessDenied", accessDeniedMessage)
			return
		}
		c.Next()
		if s := c.Writer.Status(); s == http.StatusUnauthorized || s == http.StatusForbidden {
			bc.record(ip)
		}
	}
}

// record counts one failure and logs the ban, once, at the moment it is
// imposed. Blocked requests are already in the access log; nothing more is
// logged per request.
func (bc *IPBanController) record(ip string) {
	until, n, banned := bc.store.fail(ip)
	if !banned {
		return
	}
	slog.Warn("ip banned", "ip", ip, "failures", n, "ban_until", until.UTC().Format(time.RFC3339))
}
