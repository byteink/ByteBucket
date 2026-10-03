package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

const testFailureMax = 3

// useFreshAuthState swaps the package-level session store and failure limiter
// for isolated instances driven by clk, restoring the originals on cleanup.
func useFreshAuthState(t *testing.T, clk *fakeClock) {
	t.Helper()
	prevSessions, prevFailures := sessions, failures
	sessions = newSessionStore(8, SessionIdleTimeout, SessionAbsoluteTTL, clk.now)
	failures = newFailureLimiter(testFailureMax, LoginFailureWindow, 8, clk.now)
	t.Cleanup(func() { sessions, failures = prevSessions, prevFailures })
}

// sessionEngine mounts the auth routes the way the admin router does: login
// and logout outside the admin middleware, everything else behind it.
func sessionEngine() *gin.Engine {
	r := gin.New()
	r.POST("/api/login", LoginHandler)
	r.POST("/api/logout", LogoutHandler)
	api := r.Group("/api")
	api.Use(AdminAuthMiddleware)
	api.GET("/session", SessionHandler)
	api.DELETE("/probe", func(c *gin.Context) {
		m, _ := c.Get("authMethod")
		c.String(http.StatusOK, "%v", m)
	})
	return r
}

type loginReq struct {
	ak, sk  string
	headers map[string]string
	cookie  string
}

func doLogin(r *gin.Engine, in loginReq) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"accessKey": in.ak, "secret": in.sk})
	req := httptest.NewRequest(http.MethodPost, "http://admin.local:9001/api/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range in.headers {
		req.Header.Set(k, v)
	}
	if in.cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: in.cookie})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// doWithCookie sends a same-origin browser-shaped request carrying token.
func doWithCookie(r *gin.Engine, method, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://admin.local:9001"+path, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var sameOriginHdr = map[string]string{"Sec-Fetch-Site": "same-origin"}

func sessionCookieFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response; headers=%v", SessionCookieName, w.Header())
	return nil
}

func loginOK(t *testing.T, r *gin.Engine, ak, sk string) string {
	t.Helper()
	w := doLogin(r, loginReq{ak: ak, sk: sk})
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d body=%s", w.Code, w.Body.String())
	}
	return sessionCookieFrom(t, w).Value
}

func TestLogin_IssuesHardenedSessionCookie(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	w := doLogin(sessionEngine(), loginReq{ak: ak, sk: sk})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", w.Code, w.Body.String())
	}
	raw := w.Header().Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=/api"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("Set-Cookie %q missing %s", raw, want)
		}
	}
	for _, banned := range []string{"Max-Age", "Expires", "Domain", "Secure"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("Set-Cookie %q must not carry %s on plain http", raw, banned)
		}
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: got %q, want no-store", got)
	}
	if strings.Contains(w.Body.String(), sk) {
		t.Fatal("login response echoes the secret")
	}
	var body struct {
		AccessKey string `json:"accessKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.AccessKey != ak {
		t.Fatalf("body: %s (err %v)", w.Body.String(), err)
	}
}

func TestLogin_SecureFlagBehindTLS(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	w := doLogin(sessionEngine(), loginReq{ak: ak, sk: sk, headers: map[string]string{"X-Forwarded-Proto": "https"}})
	if !sessionCookieFrom(t, w).Secure {
		t.Fatalf("cookie not Secure behind a TLS proxy: %q", w.Header().Get("Set-Cookie"))
	}
}

func TestLogin_BadSecretAndUnknownUserAreIndistinguishable(t *testing.T) {
	ak, _ := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	bad := doLogin(r, loginReq{ak: ak, sk: "wrong"})
	ghost := doLogin(r, loginReq{ak: "GHOSTACCESSKEY00001", sk: "wrong"})
	for _, w := range []*httptest.ResponseRecorder{bad, ghost} {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
		if w.Header().Get("Set-Cookie") != "" {
			t.Fatal("failed login set a cookie")
		}
	}
	if bad.Body.String() != ghost.Body.String() {
		t.Fatalf("responses leak user existence: %q vs %q", bad.Body.String(), ghost.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "Invalid admin credentials") {
		t.Fatalf("body: %s", bad.Body.String())
	}
}

func TestLogin_NonAdminRejected(t *testing.T) {
	setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	enc, err := storage.Encrypt("scopedsecret")
	if err != nil {
		t.Fatal(err)
	}
	u := &storage.User{AccessKeyID: "SCOPED", EncryptedSecret: enc,
		ACL: []storage.ACLRule{{Effect: "Allow", Buckets: []string{"b"}, Actions: []string{"*"}}}}
	if err := storage.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	w := doLogin(sessionEngine(), loginReq{ak: "SCOPED", sk: "scopedsecret"})
	if w.Code != http.StatusUnauthorized || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("non-admin login: got %d cookie=%q", w.Code, w.Header().Get("Set-Cookie"))
	}
}

func TestLogin_UndecryptableSecretIs500(t *testing.T) {
	setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	u := &storage.User{AccessKeyID: "BROKEN", EncryptedSecret: "not-ciphertext",
		ACL: []storage.ACLRule{{Effect: "Allow", Buckets: []string{"*"}, Actions: []string{"*"}}}}
	if err := storage.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	if w := doLogin(sessionEngine(), loginReq{ak: "BROKEN", sk: "x"}); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
}

func TestLogin_MalformedBody(t *testing.T) {
	setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	for _, body := range []string{"", "{", `{"accessKey":"a"}`, `{"secret":"s"}`, strings.Repeat("x", 8<<10)} {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %.20q: got %d, want 400", body, w.Code)
		}
	}
}

func TestLogin_CrossOriginRejected(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	w := doLogin(sessionEngine(), loginReq{ak: ak, sk: sk, headers: map[string]string{"Origin": "http://evil.example"}})
	if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("cross-origin login: got %d cookie=%q", w.Code, w.Header().Get("Set-Cookie"))
	}
}

func TestLogin_FailuresLockOutClientIncludingHeaderAuth(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	for i := 0; i < testFailureMax; i++ {
		if w := doLogin(r, loginReq{ak: ak, sk: "wrong"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", i, w.Code)
		}
	}
	w := doLogin(r, loginReq{ak: ak, sk: sk})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct secret while locked: got %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") != "900" {
		t.Fatalf("Retry-After: got %q, want 900", w.Header().Get("Retry-After"))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("X-Admin-AccessKey", ak)
	req.Header.Set("X-Admin-Secret", sk)
	hw := httptest.NewRecorder()
	r.ServeHTTP(hw, req)
	if hw.Code != http.StatusTooManyRequests {
		t.Fatalf("header auth while locked: got %d, want 429", hw.Code)
	}
}

func TestHeaderAuth_FailuresCountTowardLockout(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	for i := 0; i < testFailureMax; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.Header.Set("X-Admin-AccessKey", ak)
		req.Header.Set("X-Admin-Secret", "wrong")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Invalid admin credentials") {
			t.Fatalf("attempt %d: got %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := doLogin(r, loginReq{ak: ak, sk: sk}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("login after header failures: got %d, want 429", w.Code)
	}
}

func TestLogin_SuccessResetsFailureCount(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	for i := 0; i < testFailureMax-1; i++ {
		doLogin(r, loginReq{ak: ak, sk: "wrong"})
	}
	loginOK(t, r, ak, sk)
	for i := 0; i < testFailureMax-1; i++ {
		doLogin(r, loginReq{ak: ak, sk: "wrong"})
	}
	loginOK(t, r, ak, sk)
}

func TestLogin_RotatesPresentedSession(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	old := loginOK(t, r, ak, sk)
	w := doLogin(r, loginReq{ak: ak, sk: sk, cookie: old})
	fresh := sessionCookieFrom(t, w).Value
	if fresh == old {
		t.Fatal("login reused the presented session token")
	}
	if got := doWithCookie(r, http.MethodGet, "/api/session", old, sameOriginHdr); got.Code != http.StatusUnauthorized {
		t.Fatalf("pre-login session still valid: %d", got.Code)
	}
}

func TestCookieAuth_SameOriginAccepted(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	w := doWithCookie(r, http.MethodGet, "/api/session", tok, sameOriginHdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ak) {
		t.Fatalf("session: got %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session response cacheable")
	}
	w = doWithCookie(r, http.MethodDelete, "/api/probe", tok, map[string]string{"Origin": "http://admin.local:9001"})
	if w.Code != http.StatusOK || w.Body.String() != "admin-session" {
		t.Fatalf("origin-verified cookie request: got %d %q", w.Code, w.Body.String())
	}
}

func TestCookieAuth_CrossOriginRejected(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	for name, h := range map[string]map[string]string{
		"cross-site fetch":   {"Sec-Fetch-Site": "cross-site"},
		"same-site sibling":  {"Sec-Fetch-Site": "same-site"},
		"foreign origin":     {"Origin": "http://evil.example"},
		"no origin evidence": {},
	} {
		if w := doWithCookie(r, http.MethodDelete, "/api/probe", tok, h); w.Code != http.StatusForbidden {
			t.Fatalf("%s: got %d, want 403", name, w.Code)
		}
	}
}

// A browser on a plain-http, non-localhost origin (the admin UI over a LAN or
// tailnet address) sends neither Sec-Fetch-Site nor Origin on a same-origin
// GET: Fetch Metadata is only sent to potentially trustworthy URLs, and Origin
// is omitted for same-origin GET/HEAD. Safe methods change nothing, so they
// pass on the absence of evidence; explicit cross-origin evidence still fails.
func TestCookieAuth_SafeMethodWithoutEvidenceAccepted(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	if w := doWithCookie(r, http.MethodGet, "/api/session", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("plain-http same-origin GET: got %d %s", w.Code, w.Body.String())
	}
	for name, h := range map[string]map[string]string{
		"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
		"foreign origin":   {"Origin": "http://evil.example"},
	} {
		if w := doWithCookie(r, http.MethodGet, "/api/session", tok, h); w.Code != http.StatusForbidden {
			t.Fatalf("GET %s: got %d, want 403", name, w.Code)
		}
	}
}

func TestCookieAuth_ExpiredSessionRejected(t *testing.T) {
	ak, sk := setupStorage(t)
	clk := newFakeClock()
	useFreshAuthState(t, clk)
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	clk.advance(SessionIdleTimeout)
	w := doWithCookie(r, http.MethodGet, "/api/session", tok, sameOriginHdr)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("idle-expired session: got %d, want 401", w.Code)
	}
	if c := sessionCookieFrom(t, w); c.MaxAge >= 0 {
		t.Fatalf("expired session cookie not cleared: %q", w.Header().Get("Set-Cookie"))
	}
}

func TestCookieAuth_DeletedOrDemotedUserRevoked(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	demoted := loginOK(t, r, ak, sk)
	if err := storage.UpdateUserACL(ak, []storage.ACLRule{{Effect: "Allow", Buckets: []string{"b"}, Actions: []string{"*"}}}); err != nil {
		t.Fatal(err)
	}
	if w := doWithCookie(r, http.MethodGet, "/api/session", demoted, sameOriginHdr); w.Code != http.StatusUnauthorized {
		t.Fatalf("demoted admin: got %d, want 401", w.Code)
	}
	if _, ok := sessions.lookup(demoted); ok {
		t.Fatal("demoted admin's session not revoked")
	}
	if err := storage.DeleteUser(ak); err != nil {
		t.Fatal(err)
	}
	ghost := sessions.create(ak)
	if w := doWithCookie(r, http.MethodGet, "/api/session", ghost, sameOriginHdr); w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user: got %d, want 401", w.Code)
	}
}

func TestLogout_RevokesServerSideAndClearsCookie(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	w := doWithCookie(r, http.MethodPost, "/api/logout", tok, sameOriginHdr)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: got %d", w.Code)
	}
	c := sessionCookieFrom(t, w)
	if c.MaxAge >= 0 || c.Value != "" || c.Path != "/api" {
		t.Fatalf("logout did not clear the cookie: %q", w.Header().Get("Set-Cookie"))
	}
	// A copy of the token replayed after logout must be dead server-side.
	if got := doWithCookie(r, http.MethodGet, "/api/session", tok, sameOriginHdr); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session: got %d, want 401", got.Code)
	}
}

func TestLogout_CrossOriginRejectedAndSessionKept(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	tok := loginOK(t, r, ak, sk)
	if w := doWithCookie(r, http.MethodPost, "/api/logout", tok, map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout: got %d, want 403", w.Code)
	}
	if _, ok := sessions.lookup(tok); !ok {
		t.Fatal("cross-origin logout revoked the session")
	}
}

func TestLogout_WithoutSessionIsIdempotent(t *testing.T) {
	useFreshAuthState(t, newFakeClock())
	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	w := httptest.NewRecorder()
	sessionEngine().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", w.Code)
	}
}

func TestAdminAuth_HeaderAuthStillWorks(t *testing.T) {
	ak, sk := setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	req := httptest.NewRequest(http.MethodDelete, "/api/probe", nil)
	req.Header.Set("X-Admin-AccessKey", ak)
	req.Header.Set("X-Admin-Secret", sk)
	// Header auth carries its own proof of possession, so a cross-site
	// marker does not apply to it.
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	sessionEngine().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "admin" {
		t.Fatalf("header auth: got %d %q", w.Code, w.Body.String())
	}
}

func TestAdminAuth_MissingCredentials(t *testing.T) {
	setupStorage(t)
	useFreshAuthState(t, newFakeClock())
	r := sessionEngine()
	for name, h := range map[string]map[string]string{
		"nothing":     {},
		"only key":    {"X-Admin-AccessKey": "AK"},
		"only secret": {"X-Admin-Secret": "SK"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Missing admin credentials") {
			t.Fatalf("%s: got %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestSessionConstants(t *testing.T) {
	if SessionIdleTimeout != 30*time.Minute || SessionAbsoluteTTL != 8*time.Hour {
		t.Fatalf("timeouts drifted: idle=%v absolute=%v", SessionIdleTimeout, SessionAbsoluteTTL)
	}
	if sessions.capacity != sessionCap || sessionCap != 1024 {
		t.Fatalf("session cap drifted: %d", sessionCap)
	}
}
