package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// captureLogger swaps slog's default handler for a JSON handler writing into
// the returned buffer for the duration of the test. Returns a cleanup fn that
// restores the previous default — slog has no per-test scoping primitive so
// the test itself owns the lifecycle.
func captureLogger(t *testing.T, level slog.Level) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})))
	return buf, func() { slog.SetDefault(prev) }
}

// logEntry decodes a single JSON log line. Tests call it on the last line
// emitted so intermediate startup logs (none in these tests, but a safe
// pattern) do not pollute assertions.
func lastLogEntry(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("no log lines produced")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &out); err != nil {
		t.Fatalf("failed to decode log line %q: %v", lines[len(lines)-1], err)
	}
	return out
}

func newLogTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(Log())
	r.GET("/ok/:id", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/boom", func(c *gin.Context) { c.String(http.StatusInternalServerError, "boom") })
	r.GET("/missing", func(c *gin.Context) { c.String(http.StatusNotFound, "no") })
	return r
}

// A successful 2xx request must emit a single INFO line with the expected
// fields populated. This is the happy path the aggregator pipeline relies on.
func TestLogSuccessInfoLevel(t *testing.T) {
	buf, restore := captureLogger(t, slog.LevelDebug)
	defer restore()

	r := newLogTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/ok/42?secret=leak", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entry := lastLogEntry(t, buf)
	if entry["level"] != "INFO" {
		t.Fatalf("expected INFO, got %v", entry["level"])
	}
	if entry["method"] != "GET" {
		t.Fatalf("expected method GET, got %v", entry["method"])
	}
	// FullPath must collapse route params so path cardinality stays bounded.
	if entry["path"] != "/ok/:id" {
		t.Fatalf("expected path /ok/:id, got %v", entry["path"])
	}
	if entry["status"].(float64) != 200 {
		t.Fatalf("expected status 200, got %v", entry["status"])
	}
	if d, ok := entry["duration_ms"].(float64); !ok || d < 0 {
		t.Fatalf("expected non-negative duration_ms float, got %v", entry["duration_ms"])
	}
	if entry["request_id"] == nil || entry["request_id"].(string) == "" {
		t.Fatalf("expected request_id to be echoed, got %v", entry["request_id"])
	}
}

// 5xx must escalate to ERROR so alerting pipelines trigger on server faults.
func TestLogServerErrorErrorLevel(t *testing.T) {
	buf, restore := captureLogger(t, slog.LevelDebug)
	defer restore()

	r := newLogTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entry := lastLogEntry(t, buf)
	if entry["level"] != "ERROR" {
		t.Fatalf("expected ERROR for 500, got %v", entry["level"])
	}
}

// 4xx must be WARN — visible to operators without paging on client misuse.
func TestLogClientErrorWarnLevel(t *testing.T) {
	buf, restore := captureLogger(t, slog.LevelDebug)
	defer restore()

	r := newLogTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entry := lastLogEntry(t, buf)
	if entry["level"] != "WARN" {
		t.Fatalf("expected WARN for 404, got %v", entry["level"])
	}
}

// Query strings carry SigV4 signatures on the S3 surface; leaking them into
// logs would compromise credentials in aggregators. The path field must
// contain only the route template, never the raw URL.
func TestLogStripsQueryString(t *testing.T) {
	buf, restore := captureLogger(t, slog.LevelDebug)
	defer restore()

	r := newLogTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/ok/42?X-Amz-Signature=shouldnotleak", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entry := lastLogEntry(t, buf)
	path := entry["path"].(string)
	if strings.Contains(path, "?") || strings.Contains(path, "Signature") {
		t.Fatalf("path field leaked query string: %q", path)
	}
}
