package middleware

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// fakeClock lets the window and ban expiry be driven deterministically instead
// of sleeping through real seconds.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func useClock(s *banStore, clk *fakeClock) {
	s.mu.Lock()
	s.now = clk.Now
	s.mu.Unlock()
}

func banCfg(max, window, ban int) IPBanConfig {
	return IPBanConfig{Enabled: true, MaxFailures: max, WindowSeconds: window, BanSeconds: ban}
}

func storeSizes(s *banStore) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fails), len(s.bans)
}

func TestDefaultIPBanConfig(t *testing.T) {
	d := DefaultIPBanConfig()
	want := IPBanConfig{Enabled: false, MaxFailures: 20, WindowSeconds: 60, BanSeconds: 900}
	if d != want {
		t.Fatalf("defaults = %+v, want %+v", d, want)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestIPBanConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		in   IPBanConfig
		ok   bool
	}{
		{"defaults disabled", IPBanConfig{MaxFailures: 20, WindowSeconds: 60, BanSeconds: 900}, true},
		{"minimums", banCfg(1, 1, 1), true},
		{"maximums", banCfg(MaxIPBanFailures, MaxIPBanWindowSeconds, MaxIPBanSeconds), true},
		{"zero failures", banCfg(0, 60, 900), false},
		{"negative failures", banCfg(-1, 60, 900), false},
		{"too many failures", banCfg(MaxIPBanFailures+1, 60, 900), false},
		{"zero window", banCfg(20, 0, 900), false},
		{"window over 1h", banCfg(20, MaxIPBanWindowSeconds+1, 900), false},
		{"zero ban", banCfg(20, 60, 0), false},
		{"negative ban", banCfg(20, 60, -5), false},
		{"ban over 7d", banCfg(20, 60, MaxIPBanSeconds+1), false},
		{"disabled still validated", IPBanConfig{MaxFailures: 0, WindowSeconds: 60, BanSeconds: 900}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want rejection, got valid")
			}
		})
	}
}

// Normalized replaces each out-of-range field with its default independently,
// so one bad env value does not discard the operator's other choices.
func TestIPBanConfigNormalized(t *testing.T) {
	got := IPBanConfig{Enabled: true, MaxFailures: 0, WindowSeconds: 30, BanSeconds: MaxIPBanSeconds + 1}.Normalized()
	want := IPBanConfig{Enabled: true, MaxFailures: 20, WindowSeconds: 30, BanSeconds: 900}
	if got != want {
		t.Fatalf("Normalized = %+v, want %+v", got, want)
	}
	got = IPBanConfig{MaxFailures: 5, WindowSeconds: 0, BanSeconds: 60}.Normalized()
	if got.WindowSeconds != 60 || got.MaxFailures != 5 || got.BanSeconds != 60 {
		t.Fatalf("Normalized window fallback = %+v", got)
	}
	valid := banCfg(3, 10, 20)
	if valid.Normalized() != valid {
		t.Fatalf("valid config changed by Normalized: %+v", valid.Normalized())
	}
}

// TestBannableIP pins the never-ban set. Banning a proxy's internal address
// (what every request resolves to when trusted-proxy headers are misconfigured)
// would take the whole service down, so every non-public range is exempt.
func TestBannableIP(t *testing.T) {
	cases := []struct {
		in   string
		key  string
		want bool
	}{
		{"203.0.113.7", "203.0.113.7", true},
		{"8.8.8.8", "8.8.8.8", true},
		{"2001:db8::1", "2001:db8::1", true},
		{"::ffff:8.8.4.4", "8.8.4.4", true},
		{"100.63.255.255", "100.63.255.255", true},
		{"100.128.0.0", "100.128.0.0", true},
		{"127.0.0.1", "", false},
		{"::1", "", false},
		{"10.0.0.5", "", false},
		{"172.16.0.1", "", false},
		{"172.31.255.254", "", false},
		{"192.168.1.10", "", false},
		{"fc00::1", "", false},
		{"fd12:3456::1", "", false},
		{"169.254.1.1", "", false},
		{"fe80::1", "", false},
		{"fe80::1%eth0", "", false},
		{"100.64.0.1", "", false},
		{"100.127.255.255", "", false},
		{"::ffff:10.0.0.1", "", false},
		{"::ffff:100.64.1.1", "", false},
		{"0.0.0.0", "", false},
		{"::", "", false},
		{"224.0.0.1", "", false},
		{"255.255.255.255", "", false},
		{"", "", false},
		{"not-an-ip", "", false},
		{"999.1.1.1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			key, ok := bannableIP(tc.in)
			if ok != tc.want || key != tc.key {
				t.Fatalf("bannableIP(%q) = (%q, %v), want (%q, %v)", tc.in, key, ok, tc.key, tc.want)
			}
		})
	}
}

func TestBanStoreBansAtThreshold(t *testing.T) {
	s := newBanStore(banCfg(3, 60, 900), 16)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)

	for i := 1; i < 3; i++ {
		if _, _, banned := s.fail("203.0.113.1"); banned {
			t.Fatalf("banned after %d failures, threshold is 3", i)
		}
		if s.banned("203.0.113.1") {
			t.Fatalf("reported banned after %d failures", i)
		}
	}
	until, n, banned := s.fail("203.0.113.1")
	if !banned || n != 3 {
		t.Fatalf("third failure: banned=%v n=%d, want true 3", banned, n)
	}
	if want := clk.Now().Add(900 * time.Second); !until.Equal(want) {
		t.Fatalf("ban until = %v, want %v", until, want)
	}
	if !s.banned("203.0.113.1") {
		t.Fatal("IP not banned after reaching threshold")
	}
	if s.banned("203.0.113.2") {
		t.Fatal("ban leaked to another IP")
	}
	if fails, _ := storeSizes(s); fails != 0 {
		t.Fatalf("failure counter kept after ban: %d entries", fails)
	}
}

// Failures spread wider than the window never accumulate to a ban.
func TestBanStoreWindowResets(t *testing.T) {
	s := newBanStore(banCfg(3, 60, 900), 16)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)

	s.fail("203.0.113.1")
	s.fail("203.0.113.1")
	clk.Advance(60 * time.Second)
	if _, n, banned := s.fail("203.0.113.1"); banned || n != 1 {
		t.Fatalf("after window expiry: banned=%v n=%d, want false 1", banned, n)
	}
}

// A ban lasts exactly BanSeconds from the moment it was imposed. Requests made
// while banned neither extend it nor count as fresh failures.
func TestBanStoreFixedLengthNoRatchet(t *testing.T) {
	s := newBanStore(banCfg(2, 60, 10), 16)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)

	s.fail("203.0.113.1")
	s.fail("203.0.113.1")
	for i := 0; i < 9; i++ {
		clk.Advance(time.Second)
		if !s.banned("203.0.113.1") {
			t.Fatalf("ban lifted early at +%ds", i+1)
		}
		// A failure racing in while banned must be ignored, not extend the ban.
		if _, _, banned := s.fail("203.0.113.1"); banned {
			t.Fatal("failure while banned re-imposed the ban")
		}
	}
	clk.Advance(time.Second)
	if s.banned("203.0.113.1") {
		t.Fatal("ban not lifted after BanSeconds; requests while banned extended it")
	}
	if _, n, banned := s.fail("203.0.113.1"); banned || n != 1 {
		t.Fatalf("first failure after expiry: banned=%v n=%d, want false 1", banned, n)
	}
}

// TestBanStoreBounded is the Power-of-10 guard: both tables stay at or under
// the cap no matter how many distinct IPs fail.
func TestBanStoreBounded(t *testing.T) {
	s := newBanStore(banCfg(2, 60, 900), 4)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)

	for i := 0; i < 50; i++ {
		clk.Advance(time.Millisecond)
		s.fail(fmt.Sprintf("203.0.113.%d", i))
	}
	if fails, _ := storeSizes(s); fails != 4 {
		t.Fatalf("failure table = %d entries, want capped at 4", fails)
	}
	// The newest IP must have been admitted (oldest evicted), not dropped.
	if _, n, _ := s.fail("203.0.113.49"); n != 2 {
		t.Fatalf("newest IP lost its counter: n=%d", n)
	}

	for i := 0; i < 50; i++ {
		clk.Advance(time.Millisecond)
		ip := fmt.Sprintf("198.51.100.%d", i)
		s.fail(ip)
		s.fail(ip)
	}
	if _, bans := storeSizes(s); bans != 4 {
		t.Fatalf("ban table = %d entries, want capped at 4", bans)
	}
	if !s.banned("198.51.100.49") {
		t.Fatal("newest ban evicted instead of the soonest-expiring one")
	}
	if s.banned("198.51.100.0") {
		t.Fatal("oldest ban survived eviction")
	}
}

func TestBanStoreReap(t *testing.T) {
	s := newBanStore(banCfg(2, 60, 120), 16)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)

	s.fail("203.0.113.1")
	s.fail("203.0.113.2")
	s.fail("203.0.113.2")
	clk.Advance(61 * time.Second)
	s.fail("203.0.113.3")
	s.reap()
	if fails, bans := storeSizes(s); fails != 1 || bans != 1 {
		t.Fatalf("after window reap: fails=%d bans=%d, want 1 1", fails, bans)
	}
	clk.Advance(60 * time.Second)
	s.reap()
	if fails, bans := storeSizes(s); fails != 0 || bans != 0 {
		t.Fatalf("after ban expiry reap: fails=%d bans=%d, want 0 0", fails, bans)
	}
}

// Reconfiguring flushes both tables so new thresholds apply uniformly; it is
// also the operator's way to lift every active ban.
func TestBanStoreReconfigureFlushes(t *testing.T) {
	s := newBanStore(banCfg(2, 60, 900), 16)
	t.Cleanup(s.close)
	s.fail("203.0.113.1")
	s.fail("203.0.113.1")
	s.fail("203.0.113.2")
	s.reconfigure(banCfg(5, 30, 60))
	if fails, bans := storeSizes(s); fails != 0 || bans != 0 {
		t.Fatalf("reconfigure kept state: fails=%d bans=%d", fails, bans)
	}
	for i := 0; i < 4; i++ {
		if _, _, banned := s.fail("203.0.113.9"); banned {
			t.Fatal("old threshold still applied after reconfigure")
		}
	}
	if _, _, banned := s.fail("203.0.113.9"); !banned {
		t.Fatal("new threshold not applied after reconfigure")
	}
}

// The janitor must reap on its own schedule, without any request traffic.
func TestBanStoreJanitorReaps(t *testing.T) {
	s := newBanStore(banCfg(1, 60, 10), 16)
	t.Cleanup(s.close)
	clk := newFakeClock()
	useClock(s, clk)
	s.fail("203.0.113.1")
	clk.Advance(11 * time.Second)
	go s.janitor(time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, bans := storeSizes(s); bans == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("janitor did not reap the expired ban")
}

func TestBanStoreJanitorStops(t *testing.T) {
	s := newBanStore(banCfg(2, 60, 900), 16)
	s.close()
	select {
	case <-s.stop:
	default:
		t.Fatal("close did not signal the janitor")
	}
}

func TestIPBanControllerApplyCurrent(t *testing.T) {
	bc := NewIPBanController(IPBanConfig{Enabled: true, MaxFailures: 0, WindowSeconds: 60, BanSeconds: 900})
	t.Cleanup(bc.store.close)
	if got := bc.Current(); got.MaxFailures != 20 || !got.Enabled {
		t.Fatalf("constructor did not normalize: %+v", got)
	}
	bc.Apply(banCfg(4, 30, 120))
	if got := bc.Current(); got != banCfg(4, 30, 120) {
		t.Fatalf("Current after Apply = %+v", got)
	}
	bc.Apply(IPBanConfig{Enabled: true, MaxFailures: 4, WindowSeconds: -1, BanSeconds: 120})
	if got := bc.Current(); got.WindowSeconds != 60 {
		t.Fatalf("Apply did not normalize: %+v", got)
	}
}

func TestIPBanControllerCurrentNilSafe(t *testing.T) {
	var bc IPBanController
	if got := bc.Current(); got != (IPBanConfig{}) {
		t.Fatalf("zero controller Current = %+v", got)
	}
}

// banEngine mounts the middleware ahead of a handler that answers with the
// status named in ?status=, counting how many requests reached it.
func banEngine(bc *IPBanController, hits *atomic.Int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(bc.Middleware())
	r.Any("/*path", func(c *gin.Context) {
		hits.Add(1)
		code, err := strconv.Atoi(c.Query("status"))
		if err != nil {
			code = http.StatusOK
		}
		c.Status(code)
	})
	return r
}

func banDo(r *gin.Engine, peer, status string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/bkt/key?status="+status, nil)
	req.RemoteAddr = net.JoinHostPort(peer, "4000")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newTestBanController(t *testing.T, cfg IPBanConfig) *IPBanController {
	t.Helper()
	bc := NewIPBanController(cfg)
	t.Cleanup(bc.store.close)
	return bc
}

func TestIPBanMiddlewareBansAfterAuthFailures(t *testing.T) {
	bc := newTestBanController(t, banCfg(3, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)

	for i := 0; i < 2; i++ {
		banDo(r, "203.0.113.10", "401", nil)
	}
	banDo(r, "203.0.113.10", "403", nil)

	w := banDo(r, "203.0.113.10", "200", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("banned request status = %d, want 403", w.Code)
	}
	if hits.Load() != 3 {
		t.Fatalf("handler hits = %d, want 3: a banned request reached the handler", hits.Load())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("Content-Type = %q, want application/xml", ct)
	}
	var body s3ErrorBody
	if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode XML: %v body=%s", err, w.Body.String())
	}
	if body.Code != "AccessDenied" || body.RequestId == "" {
		t.Fatalf("ban body = %+v, want AccessDenied with request id", body)
	}
	if w := banDo(r, "203.0.113.11", "200", nil); w.Code != http.StatusOK {
		t.Fatalf("unrelated IP status = %d, want 200", w.Code)
	}
}

func TestIPBanMiddlewareJSONWhenRequested(t *testing.T) {
	bc := newTestBanController(t, banCfg(1, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)
	banDo(r, "203.0.113.10", "401", nil)
	w := banDo(r, "203.0.113.10", "200", map[string]string{"Accept": "application/json"})
	var body s3ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON: %v body=%s", err, w.Body.String())
	}
	if w.Code != http.StatusForbidden || body.Code != "AccessDenied" {
		t.Fatalf("JSON ban = %d %+v", w.Code, body)
	}
}

// Only 401 and 403 count. 404 in particular must never ban: imgproxy and
// similar clients routinely fetch objects that were deleted.
func TestIPBanMiddlewareIgnoresOtherStatuses(t *testing.T) {
	bc := newTestBanController(t, banCfg(2, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)
	for _, status := range []string{"200", "304", "400", "404", "412", "429", "500", "503"} {
		for i := 0; i < 5; i++ {
			banDo(r, "203.0.113.20", status, nil)
		}
	}
	if w := banDo(r, "203.0.113.20", "200", nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d after non-auth failures, want 200 (not banned)", w.Code)
	}
}

func TestIPBanMiddlewareDisabledNeverBans(t *testing.T) {
	cfg := banCfg(1, 60, 900)
	cfg.Enabled = false
	bc := newTestBanController(t, cfg)
	var hits atomic.Int64
	r := banEngine(bc, &hits)
	for i := 0; i < 10; i++ {
		banDo(r, "203.0.113.30", "401", nil)
	}
	if w := banDo(r, "203.0.113.30", "200", nil); w.Code != http.StatusOK {
		t.Fatalf("disabled ban blocked a request: %d", w.Code)
	}
	if fails, bans := storeSizes(bc.store); fails != 0 || bans != 0 {
		t.Fatalf("disabled path touched the store: fails=%d bans=%d", fails, bans)
	}
}

// With trusted-proxy headers misconfigured every request resolves to the
// proxy's internal address. That address must never be banned, or one
// scanner would take the whole service offline.
func TestIPBanMiddlewareNeverBansProxyAddress(t *testing.T) {
	storage.SetTrustedProxy(storage.TrustedProxyConfig{Headers: []string{"X-Forwarded-For"}})
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })
	bc := newTestBanController(t, banCfg(1, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)

	for _, peer := range []string{"172.18.0.2", "10.0.0.7", "127.0.0.1", "100.64.3.4", "fd00::2"} {
		for i := 0; i < 5; i++ {
			banDo(r, peer, "401", nil)
			banDo(r, peer, "401", map[string]string{"X-Forwarded-For": "192.168.5.5"})
		}
		if w := banDo(r, peer, "200", nil); w.Code != http.StatusOK {
			t.Fatalf("non-public peer %s banned: %d", peer, w.Code)
		}
	}
	if _, bans := storeSizes(bc.store); bans != 0 {
		t.Fatalf("ban table has %d entries for non-public addresses", bans)
	}

	// The real client behind a correctly configured proxy is still bannable.
	xff := map[string]string{"X-Forwarded-For": "203.0.113.40"}
	banDo(r, "172.18.0.2", "401", xff)
	if w := banDo(r, "172.18.0.2", "200", xff); w.Code != http.StatusForbidden {
		t.Fatalf("public client behind proxy not banned: %d", w.Code)
	}
	if w := banDo(r, "172.18.0.2", "200", nil); w.Code != http.StatusOK {
		t.Fatalf("proxy itself caught by the client's ban: %d", w.Code)
	}
}

// A banned request must not reset or extend the ban, nor count as a failure.
func TestIPBanMiddlewareBlockedRequestsDoNotCount(t *testing.T) {
	bc := newTestBanController(t, banCfg(2, 60, 10))
	clk := newFakeClock()
	useClock(bc.store, clk)
	var hits atomic.Int64
	r := banEngine(bc, &hits)

	banDo(r, "203.0.113.50", "401", nil)
	banDo(r, "203.0.113.50", "401", nil)
	for i := 0; i < 9; i++ {
		clk.Advance(time.Second)
		if w := banDo(r, "203.0.113.50", "401", nil); w.Code != http.StatusForbidden {
			t.Fatalf("ban lifted early at +%ds: %d", i+1, w.Code)
		}
	}
	clk.Advance(time.Second)
	if w := banDo(r, "203.0.113.50", "401", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("ban not lifted on schedule: %d", w.Code)
	}
	if w := banDo(r, "203.0.113.50", "200", nil); w.Code != http.StatusOK {
		t.Fatalf("blocked requests counted as failures (re-banned): %d", w.Code)
	}
}

func TestIPBanMiddlewareLogsBanOnce(t *testing.T) {
	buf, restore := captureLogger(t, slog.LevelDebug)
	defer restore()
	bc := newTestBanController(t, banCfg(2, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)

	for i := 0; i < 6; i++ {
		banDo(r, "203.0.113.60", "401", nil)
	}
	var lines []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) == nil && m["msg"] == "ip banned" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("ban log lines = %d, want exactly 1; log=%s", len(lines), buf.String())
	}
	m := lines[0]
	if m["ip"] != "203.0.113.60" || m["failures"] != float64(2) {
		t.Fatalf("ban log fields = %v", m)
	}
	if _, err := time.Parse(time.RFC3339, fmt.Sprint(m["ban_until"])); err != nil {
		t.Fatalf("ban_until not RFC3339: %v (%v)", m["ban_until"], err)
	}
}

// Exercises the store from many goroutines; meaningful under -race.
func TestIPBanMiddlewareConcurrent(t *testing.T) {
	bc := newTestBanController(t, banCfg(5, 60, 900))
	var hits atomic.Int64
	r := banEngine(bc, &hits)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ip := fmt.Sprintf("203.0.113.%d", g%4)
			for i := 0; i < 50; i++ {
				banDo(r, ip, "401", nil)
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			bc.Apply(banCfg(5, 60, 900))
			bc.store.reap()
		}
	}()
	wg.Wait()
	if fails, bans := storeSizes(bc.store); fails > maxBanEntries || bans > maxBanEntries {
		t.Fatalf("tables exceeded cap: %d %d", fails, bans)
	}
}
