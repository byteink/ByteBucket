package tests

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The embedded UI is built inside the Docker image by the node stage, so
// these tests observe the real SPA bundle rather than the in-repo placeholder.

func TestE2E_WebUI_IndexLoads(t *testing.T) {
	res, err := http.Get(adminURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "<html") && !strings.Contains(lower, "<!doctype") {
		t.Fatalf("body does not look like HTML: %q", string(body))
	}
}

var scriptSrcRE = regexp.MustCompile(`<script[^>]+type=["']module["'][^>]+src=["']([^"']+)["']`)

func TestE2E_WebUI_AssetsServed(t *testing.T) {
	res, err := http.Get(adminURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	m := scriptSrcRE.FindSubmatch(body)
	if m == nil {
		t.Skip("index has no module script tag; UI bundle not present")
	}
	src := string(m[1])
	if !strings.HasPrefix(src, "/") {
		src = "/" + src
	}

	assetRes, err := http.Get(adminURL + src)
	if err != nil {
		t.Fatalf("GET %s: %v", src, err)
	}
	defer func() { _ = assetRes.Body.Close() }()

	if assetRes.StatusCode != http.StatusOK {
		t.Fatalf("asset status: got %d, want 200", assetRes.StatusCode)
	}
	ct := assetRes.Header.Get("Content-Type")
	if !strings.Contains(ct, "javascript") && !strings.Contains(ct, "ecmascript") {
		t.Fatalf("asset content-type: got %q, want javascript", ct)
	}
}

func TestE2E_WebUI_SPAFallback(t *testing.T) {
	res, err := http.Get(adminURL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (SPA fallback)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
}

func TestE2E_WebUI_AdminEndpointsStillProtected(t *testing.T) {
	// Without auth: rejected.
	unauthed, err := http.Get(adminURL + "/users")
	if err != nil {
		t.Fatalf("GET /users: %v", err)
	}
	_ = unauthed.Body.Close()
	if unauthed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthed /users: got %d, want 401", unauthed.StatusCode)
	}

	// With auth: accepted.
	req, err := http.NewRequest(http.MethodGet, adminURL+"/users", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET /users: %v", err)
	}
	_ = authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authed /users: got %d, want 200", authed.StatusCode)
	}
}
