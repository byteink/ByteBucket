package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced time source so expiry tests are exact and
// never sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func newTestStore(capacity int, clk *fakeClock) *sessionStore {
	return newSessionStore(capacity, 30*time.Minute, 8*time.Hour, clk.now)
}

func mustCreate(t *testing.T, s *sessionStore, ak string) string {
	t.Helper()
	return s.create(ak)
}

func TestSessionStore_CreateIssues256BitRandomToken(t *testing.T) {
	s := newTestStore(8, newFakeClock())
	a := mustCreate(t, s, "AK")
	b := mustCreate(t, s, "AK")
	if a == b {
		t.Fatal("two sessions share a token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("token entropy: got %d bytes, want 32", len(raw))
	}
}

func TestSessionStore_StoresOnlyTokenDigest(t *testing.T) {
	s := newTestStore(8, newFakeClock())
	tok := mustCreate(t, s, "AK")
	if _, ok := s.byDigest[sha256.Sum256([]byte(tok))]; !ok {
		t.Fatal("session not keyed by the token's SHA-256 digest")
	}
	for k, v := range s.byDigest {
		if string(k[:]) == tok || v.accessKey == tok {
			t.Fatal("raw token retained in the store")
		}
	}
}

func TestSessionStore_LookupValidToken(t *testing.T) {
	s := newTestStore(8, newFakeClock())
	tok := mustCreate(t, s, "AK1")
	ak, ok := s.lookup(tok)
	if !ok || ak != "AK1" {
		t.Fatalf("lookup: got (%q,%v), want (AK1,true)", ak, ok)
	}
}

func TestSessionStore_LookupUnknownAndMalformedToken(t *testing.T) {
	s := newTestStore(8, newFakeClock())
	mustCreate(t, s, "AK1")
	for _, tok := range []string{"", "short", base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if _, ok := s.lookup(tok); ok {
			t.Fatalf("lookup accepted unknown token %q", tok)
		}
	}
}

func TestSessionStore_IdleTimeoutExpires(t *testing.T) {
	clk := newFakeClock()
	s := newTestStore(8, clk)
	tok := mustCreate(t, s, "AK")
	clk.advance(30 * time.Minute)
	if _, ok := s.lookup(tok); ok {
		t.Fatal("session survived its idle timeout")
	}
	if len(s.byDigest) != 0 {
		t.Fatal("expired session not removed on lookup")
	}
}

func TestSessionStore_ActivitySlidesIdleWindow(t *testing.T) {
	clk := newFakeClock()
	s := newTestStore(8, clk)
	tok := mustCreate(t, s, "AK")
	for i := 0; i < 4; i++ {
		clk.advance(29 * time.Minute)
		if _, ok := s.lookup(tok); !ok {
			t.Fatalf("active session expired after %d idle-window slides", i+1)
		}
	}
}

func TestSessionStore_AbsoluteTimeoutExpiresActiveSession(t *testing.T) {
	clk := newFakeClock()
	s := newTestStore(8, clk)
	tok := mustCreate(t, s, "AK")
	// Stay active (never idle) for the whole absolute lifetime.
	for elapsed := time.Duration(0); elapsed < 8*time.Hour-20*time.Minute; elapsed += 20 * time.Minute {
		clk.advance(20 * time.Minute)
		if _, ok := s.lookup(tok); !ok {
			t.Fatalf("session expired early at %v", elapsed+20*time.Minute)
		}
	}
	clk.advance(20 * time.Minute)
	if _, ok := s.lookup(tok); ok {
		t.Fatal("session survived its absolute lifetime")
	}
}

func TestSessionStore_RevokeRemovesSession(t *testing.T) {
	s := newTestStore(8, newFakeClock())
	tok := mustCreate(t, s, "AK")
	other := mustCreate(t, s, "AK")
	s.revoke(tok)
	if _, ok := s.lookup(tok); ok {
		t.Fatal("revoked session still valid")
	}
	if _, ok := s.lookup(other); !ok {
		t.Fatal("revoke removed an unrelated session")
	}
	s.revoke("not-a-token") // must be a harmless no-op
}

func TestSessionStore_CapPrunesExpiredFirst(t *testing.T) {
	clk := newFakeClock()
	s := newTestStore(2, clk)
	stale := mustCreate(t, s, "AK")
	clk.advance(20 * time.Minute)
	live := mustCreate(t, s, "AK")
	clk.advance(15 * time.Minute) // stale is now idle-expired, live is not
	fresh := mustCreate(t, s, "AK")
	if len(s.byDigest) != 2 {
		t.Fatalf("store size: got %d, want cap 2", len(s.byDigest))
	}
	if _, ok := s.lookup(live); !ok {
		t.Fatal("live session evicted while an expired one was available")
	}
	if _, ok := s.lookup(fresh); !ok {
		t.Fatal("new session not stored")
	}
	if _, ok := s.lookup(stale); ok {
		t.Fatal("expired session still present")
	}
}

func TestSessionStore_CapEvictsLeastRecentlyUsed(t *testing.T) {
	clk := newFakeClock()
	s := newTestStore(2, clk)
	a := mustCreate(t, s, "AK")
	clk.advance(time.Minute)
	b := mustCreate(t, s, "AK")
	clk.advance(time.Minute)
	if _, ok := s.lookup(a); !ok { // a is now the most recently used
		t.Fatal("a should be valid")
	}
	clk.advance(time.Minute)
	c := mustCreate(t, s, "AK")
	if len(s.byDigest) != 2 {
		t.Fatalf("store size: got %d, want cap 2", len(s.byDigest))
	}
	if _, ok := s.lookup(b); ok {
		t.Fatal("least recently used session survived eviction")
	}
	for _, tok := range []string{a, c} {
		if _, ok := s.lookup(tok); !ok {
			t.Fatal("recently used session evicted")
		}
	}
}

func TestFailureLimiter_LocksAfterMaxFailures(t *testing.T) {
	clk := newFakeClock()
	l := newFailureLimiter(3, 15*time.Minute, 8, clk.now)
	for i := 0; i < 3; i++ {
		if _, locked := l.blocked("1.2.3.4"); locked {
			t.Fatalf("locked after only %d failures", i)
		}
		l.fail("1.2.3.4")
	}
	retry, locked := l.blocked("1.2.3.4")
	if !locked {
		t.Fatal("not locked after max failures")
	}
	if retry != 15*time.Minute {
		t.Fatalf("retry-after: got %v, want 15m", retry)
	}
	if _, locked := l.blocked("5.6.7.8"); locked {
		t.Fatal("lockout leaked to another client")
	}
}

func TestFailureLimiter_WindowExpiryUnlocks(t *testing.T) {
	clk := newFakeClock()
	l := newFailureLimiter(2, 15*time.Minute, 8, clk.now)
	l.fail("ip")
	l.fail("ip")
	clk.advance(10 * time.Minute)
	if retry, locked := l.blocked("ip"); !locked || retry != 5*time.Minute {
		t.Fatalf("got (%v,%v), want (5m,true)", retry, locked)
	}
	clk.advance(5 * time.Minute)
	if _, locked := l.blocked("ip"); locked {
		t.Fatal("still locked after the window elapsed")
	}
	l.fail("ip") // a failure after expiry starts a fresh window
	if _, locked := l.blocked("ip"); locked {
		t.Fatal("stale count carried into a new window")
	}
}

func TestFailureLimiter_ResetClearsCount(t *testing.T) {
	l := newFailureLimiter(2, 15*time.Minute, 8, newFakeClock().now)
	l.fail("ip")
	l.reset("ip")
	l.fail("ip")
	if _, locked := l.blocked("ip"); locked {
		t.Fatal("reset did not clear the failure count")
	}
}

func TestFailureLimiter_CapIsBounded(t *testing.T) {
	clk := newFakeClock()
	l := newFailureLimiter(2, 15*time.Minute, 2, clk.now)
	l.fail("a")
	clk.advance(time.Minute)
	l.fail("b")
	clk.advance(time.Minute)
	l.fail("c") // full: evicts the oldest window ("a")
	if len(l.byKey) != 2 {
		t.Fatalf("limiter size: got %d, want cap 2", len(l.byKey))
	}
	if _, ok := l.byKey["a"]; ok {
		t.Fatal("oldest window not evicted")
	}
	clk.advance(15 * time.Minute) // b and c expire
	l.fail("d")
	if len(l.byKey) != 1 {
		t.Fatalf("expired windows not pruned: size %d", len(l.byKey))
	}
}

func TestOriginVerdict(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    originVerdict
	}{
		{"fetch metadata same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, originSame},
		{"fetch metadata cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, originCross},
		{"fetch metadata same-site sibling", map[string]string{"Sec-Fetch-Site": "same-site"}, originCross},
		{"fetch metadata none (typed URL)", map[string]string{"Sec-Fetch-Site": "none"}, originCross},
		{"fetch metadata wins over origin", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://admin.local:9001"}, originCross},
		{"origin matches host", map[string]string{"Origin": "http://admin.local:9001"}, originSame},
		{"origin host case-insensitive", map[string]string{"Origin": "https://ADMIN.local:9001"}, originSame},
		{"origin other host", map[string]string{"Origin": "http://evil.example"}, originCross},
		{"origin suffix trick", map[string]string{"Origin": "http://admin.local:9001.evil.example"}, originCross},
		{"origin null", map[string]string{"Origin": "null"}, originCross},
		{"no browser headers", map[string]string{}, originUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://admin.local:9001/api/x", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := checkOrigin(r); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSecretsEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"secret", "secret", true},
		{"secret", "secreT", false},
		{"secret", "secret-longer", false},
		{"", "secret", false},
		{"", "", true},
	}
	for _, tc := range cases {
		if got := secretsEqual(tc.a, tc.b); got != tc.want {
			t.Fatalf("secretsEqual(%q,%q)=%v want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRequestIsHTTPS(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "http://h/api", nil)
	if requestIsHTTPS(plain) {
		t.Fatal("plain http reported as https")
	}
	tls := httptest.NewRequest(http.MethodGet, "https://h/api", nil)
	if !requestIsHTTPS(tls) {
		t.Fatal("direct TLS not detected")
	}
	proxied := httptest.NewRequest(http.MethodGet, "http://h/api", nil)
	proxied.Header.Set("X-Forwarded-Proto", "HTTPS, http")
	if !requestIsHTTPS(proxied) {
		t.Fatal("TLS-terminating proxy not honoured")
	}
	downgraded := httptest.NewRequest(http.MethodGet, "http://h/api", nil)
	downgraded.Header.Set("X-Forwarded-Proto", "http")
	if requestIsHTTPS(downgraded) {
		t.Fatal("X-Forwarded-Proto: http reported as https")
	}
}
