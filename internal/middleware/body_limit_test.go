package middleware

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	s3Path      = "/bucket/key"
	adminPath   = "/s3/bucket/key"
	wantCodeFmt = "code = %q, want EntityTooLarge"
)

// buildHandler composes the same middleware chain the real servers use: a
// Gin engine wrapped by the net/http-level BodyLimit. The drain handler
// mirrors what the production S3 object handler does (io.Copy on
// Request.Body) so a regression in the limiter surfaces here the same way
// it would in prod.
func buildHandler(limit int64) http.Handler {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.PUT("/*path", func(c *gin.Context) {
		if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
			// Handlers in production translate this to a generic 500. The
			// limiter must have already written the 413, so this call is a
			// no-op on the wire — the tests assert that invariant.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": "InternalError"})
			return
		}
		c.Status(http.StatusOK)
	})
	return BodyLimit(r, limit)
}

// TestBodyLimitUnderLimit confirms the middleware is transparent for requests
// that stay within budget. Without this guard, a too-aggressive implementation
// could 413 on every request and we would never notice in isolated tests.
func TestBodyLimitUnderLimit(t *testing.T) {
	h := buildHandler(1024)
	body := bytes.Repeat([]byte("a"), 100)
	req := httptest.NewRequest(http.MethodPut, s3Path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
}

// TestBodyLimitOverLimitXML verifies the S3 contract on the SigV4 surface: a
// breach must return 413 with an EntityTooLarge code in XML that AWS SDKs can
// pattern-match, not an opaque 500.
func TestBodyLimitOverLimitXML(t *testing.T) {
	h := buildHandler(50)
	body := bytes.Repeat([]byte("a"), 200)
	req := httptest.NewRequest(http.MethodPut, s3Path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Fatalf("content-type = %q, want xml", ct)
	}
	var got s3ErrorBody
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal xml: %v; body=%q", err, w.Body.String())
	}
	if got.Code != "EntityTooLarge" {
		t.Fatalf(wantCodeFmt, got.Code)
	}
	if got.RequestId == "" {
		t.Fatal("RequestId missing from 413 body; RequestIDMiddleware must reach the error path")
	}
}

// TestBodyLimitOverLimitJSON verifies the admin surface: paths that the
// respond helpers treat as JSON (/s3, /users, /cors) must get JSON-shaped
// errors so the SPA can render them without an XML parser.
func TestBodyLimitOverLimitJSON(t *testing.T) {
	h := buildHandler(50)
	body := bytes.Repeat([]byte("a"), 200)
	req := httptest.NewRequest(http.MethodPut, adminPath, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("content-type = %q, want json", ct)
	}
	var got struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal json: %v; body=%q", err, w.Body.String())
	}
	if got.Code != "EntityTooLarge" {
		t.Fatalf(wantCodeFmt, got.Code)
	}
}

// TestBodyLimitHandlerFollowUpSuppressed asserts that a handler which tries
// to write its own error after the limiter has fired cannot corrupt the
// response body. This is the specific regression the silencableWriter exists
// to prevent, so it earns its own test even though TestBodyLimitOverLimitXML
// would notice a gross breakage.
func TestBodyLimitHandlerFollowUpSuppressed(t *testing.T) {
	h := buildHandler(50)
	body := bytes.Repeat([]byte("a"), 200)
	req := httptest.NewRequest(http.MethodPut, s3Path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// The XML body must parse cleanly — a trailing 500-JSON payload from the
	// handler would break the XML decoder below.
	var got s3ErrorBody
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body was corrupted by handler follow-up: %v; body=%q", err, w.Body.String())
	}
	if got.Code != "EntityTooLarge" {
		t.Fatalf(wantCodeFmt, got.Code)
	}
}

// TestBodyLimitZeroDisables documents the behaviour of a zero or negative
// limit: the middleware short-circuits rather than 413-ing every request.
// Callers that do not want a limit on a particular surface can omit the
// middleware entirely; this guard exists so a mis-configured constant cannot
// silently lock out every client.
func TestBodyLimitZeroDisables(t *testing.T) {
	h := buildHandler(0)
	body := bytes.Repeat([]byte("a"), 1024)
	req := httptest.NewRequest(http.MethodPut, s3Path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (limit disabled)", w.Code)
	}
}
