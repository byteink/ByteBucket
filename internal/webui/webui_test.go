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
