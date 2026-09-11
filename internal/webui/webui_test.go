package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doGet(t *testing.T, h http.Handler, p string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, p, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

func TestHandler_ServesIndexAtRoot(t *testing.T) {
	res := doGet(t, Handler(), "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", res.StatusCode)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
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

func TestHandler_SPAFallbackReturnsIndex(t *testing.T) {
	res := doGet(t, Handler(), "/login")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
}

func TestHandler_RejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", w.Result().StatusCode)
	}
}

func TestHandler_BlocksPathTraversal(t *testing.T) {
	// `..` components are stripped by path.Clean before we dispatch, and any
	// path containing `..` after the leading slash is forced to fall back to
	// index.html rather than escaping the embedded FS.
	res := doGet(t, Handler(), "/../secret")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (index fallback)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
}

func cspFor(t *testing.T, origin string) string {
	t.Helper()
	SetMediaOrigin(origin)
	t.Cleanup(func() { SetMediaOrigin("") })
	return doGet(t, Handler(), "/").Header.Get("Content-Security-Policy")
}

func TestHandler_CSPAllowsPublicOriginForMedia(t *testing.T) {
	csp := cspFor(t, "https://bb.example.com/base/")
	for _, want := range []string{
		"img-src 'self' data: blob: https://bb.example.com;",
		"media-src 'self' blob: https://bb.example.com;",
		"frame-src 'self' blob: https://bb.example.com;",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
}

func TestHandler_CSPWithoutPublicOriginStaysSameOrigin(t *testing.T) {
	csp := cspFor(t, "")
	for _, want := range []string{"img-src 'self' data: blob:;", "media-src 'self' blob:;", "frame-src 'self' blob:;", "script-src 'self';"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
}

func TestHandler_CSPIgnoresMalformedOrigin(t *testing.T) {
	for _, bad := range []string{"garbage", "ftp://x", "http://", "javascript:alert(1)"} {
		csp := cspFor(t, bad)
		if strings.Contains(csp, "garbage") || strings.Contains(csp, "ftp:") || strings.Contains(csp, "javascript") {
			t.Fatalf("malformed origin %q leaked into CSP %q", bad, csp)
		}
		if !strings.Contains(csp, "media-src 'self' blob:;") {
			t.Fatalf("malformed origin %q changed the default CSP %q", bad, csp)
		}
	}
}
