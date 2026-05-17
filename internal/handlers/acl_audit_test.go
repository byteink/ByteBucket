package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// captureSlog redirects the global slog logger at a buffer for the life of
// the test. Returns the buffer so callers can search emitted lines.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &buf
}

// findAuditLine pulls the first acl_change line out of a captured slog
// buffer and returns it parsed. Fails the test if no such line exists.
func findAuditLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["msg"] == "acl_change" {
			return m
		}
	}
	t.Fatalf("no acl_change line in log:\n%s", buf.String())
	return nil
}

func TestAuditACLChange_BucketAcl(t *testing.T) {
	seedACLBucket(t, "auditbkt")
	buf := captureSlog(t)

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/auditbkt?acl", nil)
	req.Header.Set("x-amz-acl", "public-read")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	entry := findAuditLine(t, buf)
	if entry["resource"] != "bucket" {
		t.Fatalf("resource=%v want bucket", entry["resource"])
	}
	if entry["from"] != storage.ACLPrivate || entry["to"] != storage.ACLPublicRead {
		t.Fatalf("from=%v to=%v", entry["from"], entry["to"])
	}
	if entry["bucket"] != "auditbkt" {
		t.Fatalf("bucket=%v", entry["bucket"])
	}
}

func TestAuditACLChange_ObjectAcl(t *testing.T) {
	dir := seedACLBucket(t, "auditbkt")
	objPath := filepath.Join(dir, "auditbkt", "photo.jpg")
	if err := os.WriteFile(objPath, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := captureSlog(t)

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/auditbkt/photo.jpg?acl", nil)
	req.Header.Set("x-amz-acl", "public-read")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	entry := findAuditLine(t, buf)
	if entry["resource"] != "object" {
		t.Fatalf("resource=%v want object", entry["resource"])
	}
	if entry["key"] != "photo.jpg" {
		t.Fatalf("key=%v", entry["key"])
	}
	if entry["to"] != storage.ACLPublicRead {
		t.Fatalf("to=%v want public-read", entry["to"])
	}
}

// Re-applying the same canned value must NOT emit an audit line — that
// would flood the log every time the UI refreshes a "public" toggle.
func TestAuditACLChange_NoEntryWhenUnchanged(t *testing.T) {
	seedACLBucket(t, "noopbkt")
	r := newACLTestEngine()
	// First PUT establishes public-read.
	w := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPut, "/noopbkt?acl", nil)
	req1.Header.Set("x-amz-acl", "public-read")
	r.ServeHTTP(w, req1)
	if w.Code != http.StatusOK {
		t.Fatalf("first put: %d", w.Code)
	}

	// Capture only what comes after, then re-apply.
	buf := captureSlog(t)
	w = httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPut, "/noopbkt?acl", nil)
	req2.Header.Set("x-amz-acl", "public-read")
	r.ServeHTTP(w, req2)
	if w.Code != http.StatusOK {
		t.Fatalf("re-apply: %d", w.Code)
	}
	if strings.Contains(buf.String(), "acl_change") {
		t.Fatalf("re-apply of same canned value should not emit audit line, got:\n%s", buf.String())
	}
}

// CreateBucket with x-amz-acl=public-read must also emit an audit line so
// "this bucket was born public" is visible in the log.
func TestAuditACLChange_CreateBucketWithCannedACL(t *testing.T) {
	seedACLBucket(t, "")
	buf := captureSlog(t)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket", CreateBucketHandler)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/birthpublic", nil)
	req.Header.Set("x-amz-acl", "public-read")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}

	entry := findAuditLine(t, buf)
	if entry["bucket"] != "birthpublic" || entry["to"] != storage.ACLPublicRead {
		t.Fatalf("entry=%v", entry)
	}
}
