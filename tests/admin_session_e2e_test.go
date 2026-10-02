package tests

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const sessionCookie = "bb_admin_session"

// sessionDo sends one request to the admin port without a cookie jar, so each
// test controls exactly which cookie and origin evidence the server sees.
func sessionDo(t *testing.T, method, path, body string, headers map[string]string, token string) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, adminURL+path, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res, string(b)
}

func adminLoginBody() string {
	return `{"accessKey":"` + adminCreds.AccessKeyID + `","secret":"` + adminCreds.SecretAccessKey + `"}`
}

func e2eLogin(t *testing.T, headers map[string]string) (*http.Response, string) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		h[k] = v
	}
	res, body := sessionDo(t, http.MethodPost, "/api/login", adminLoginBody(), h, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d body=%s", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return res, c.Value
		}
	}
	t.Fatalf("login set no session cookie: %v", res.Header.Values("Set-Cookie"))
	return nil, ""
}

var browserSameOrigin = map[string]string{"Sec-Fetch-Site": "same-origin", "Accept": "application/json"}

func TestE2E_Session_LoginCookieFlags(t *testing.T) {
	res, _ := e2eLogin(t, nil)
	raw := res.Header.Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=/api"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("Set-Cookie %q missing %s", raw, want)
		}
	}
	if strings.Contains(raw, "Max-Age") || strings.Contains(raw, "Expires") || strings.Contains(raw, "Secure") {
		t.Fatalf("plain-http session cookie must be non-persistent and not Secure: %q", raw)
	}
	tls, _ := e2eLogin(t, map[string]string{"X-Forwarded-Proto": "https"})
	if !strings.Contains(tls.Header.Get("Set-Cookie"), "Secure") {
		t.Fatalf("cookie behind TLS proxy not Secure: %q", tls.Header.Get("Set-Cookie"))
	}
}

func TestE2E_Session_BadLoginDoesNotRevealUsers(t *testing.T) {
	h := map[string]string{"Content-Type": "application/json"}
	wrong, wrongBody := sessionDo(t, http.MethodPost, "/api/login",
		`{"accessKey":"`+adminCreds.AccessKeyID+`","secret":"wrong"}`, h, "")
	ghost, ghostBody := sessionDo(t, http.MethodPost, "/api/login",
		`{"accessKey":"GHOSTACCESSKEY00001","secret":"wrong"}`, h, "")
	if wrong.StatusCode != http.StatusUnauthorized || ghost.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad logins: got %d / %d, want 401", wrong.StatusCode, ghost.StatusCode)
	}
	if !strings.Contains(wrongBody, "Invalid admin credentials") || !strings.Contains(ghostBody, "Invalid admin credentials") {
		t.Fatalf("bad login bodies differ or leak: %q vs %q", wrongBody, ghostBody)
	}
	if wrong.Header.Get("Set-Cookie") != "" || ghost.Header.Get("Set-Cookie") != "" {
		t.Fatal("failed login set a cookie")
	}
}

// The cookie must drive both the admin API and the /api/s3 storage proxy, the
// two surfaces the web UI uses.
func TestE2E_Session_CookieDrivesAdminAndS3Proxy(t *testing.T) {
	_, tok := e2eLogin(t, nil)
	res, body := sessionDo(t, http.MethodGet, "/api/session", "", browserSameOrigin, tok)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, adminCreds.AccessKeyID) {
		t.Fatalf("session: got %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, adminCreds.SecretAccessKey) {
		t.Fatal("session response leaks the secret")
	}
	if res, body := sessionDo(t, http.MethodPut, "/api/s3/session-e2e", "", browserSameOrigin, tok); res.StatusCode != http.StatusOK {
		t.Fatalf("create bucket via cookie: got %d %s", res.StatusCode, body)
	}
	if res, body := sessionDo(t, http.MethodPut, "/api/s3/session-e2e/hello.txt", "hi", browserSameOrigin, tok); res.StatusCode != http.StatusOK {
		t.Fatalf("put object via cookie: got %d %s", res.StatusCode, body)
	}
	if res, body := sessionDo(t, http.MethodGet, "/api/s3/session-e2e/hello.txt", "", browserSameOrigin, tok); res.StatusCode != http.StatusOK || body != "hi" {
		t.Fatalf("get object via cookie: got %d %q", res.StatusCode, body)
	}
	if res, _ := sessionDo(t, http.MethodGet, "/api/users", "", browserSameOrigin, tok); res.StatusCode != http.StatusOK {
		t.Fatalf("admin API via cookie: got %d", res.StatusCode)
	}
}

func TestE2E_Session_CrossOriginCookieRejected(t *testing.T) {
	_, tok := e2eLogin(t, nil)
	for name, h := range map[string]map[string]string{
		"cross-site fetch":   {"Sec-Fetch-Site": "cross-site"},
		"foreign origin":     {"Origin": "http://evil.example"},
		"no origin evidence": {},
	} {
		if res, _ := sessionDo(t, http.MethodPut, "/api/s3/session-csrf", "", h, tok); res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: got %d, want 403", name, res.StatusCode)
		}
	}
	if res, _ := sessionDo(t, http.MethodGet, "/api/s3/session-csrf", "", browserSameOrigin, tok); res.StatusCode != http.StatusNotFound {
		t.Fatalf("rejected cross-origin PUT still created the bucket: got %d", res.StatusCode)
	}
}

func TestE2E_Session_LogoutRevokesServerSide(t *testing.T) {
	_, tok := e2eLogin(t, nil)
	res, _ := sessionDo(t, http.MethodPost, "/api/logout", "", browserSameOrigin, tok)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout did not clear the cookie: %q", res.Header.Get("Set-Cookie"))
	}
	if res, _ := sessionDo(t, http.MethodGet, "/api/session", "", browserSameOrigin, tok); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session replay: got %d, want 401", res.StatusCode)
	}
}

func TestE2E_WebUI_StrictCSP(t *testing.T) {
	res, err := http.Get(adminURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	_ = res.Body.Close()
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "style-src 'self';", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-") {
		t.Fatalf("CSP %q allows unsafe-*", csp)
	}
}
