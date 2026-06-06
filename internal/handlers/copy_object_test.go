package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// newCopyEngine wires PUT /:bucket/*objectKey to dispatch to CopyObjectHandler
// when x-amz-copy-source is present, mirroring production dispatchObjectPUT,
// otherwise to UploadObjectHandler. No auth: the handler carries the contract.
func newCopyEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket/*objectKey", func(c *gin.Context) {
		if c.GetHeader("x-amz-copy-source") != "" {
			CopyObjectHandler(c)
			return
		}
		UploadObjectHandler(c)
	})
	return r
}

func copyReq(t *testing.T, r *gin.Engine, bucket, key, source string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/"+bucket+"/"+key, nil)
	req.Header.Set("x-amz-copy-source", source)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func readMeta(t *testing.T, dir, bucket, key string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, bucket, key+".meta"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	return m
}

func TestCopyObject_CopiesBytesAndETag(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "src.txt", "hello world")
	// Seed source metadata so the COPY directive has something to carry over.
	if err := os.WriteFile(filepath.Join(dir, "b1", "src.txt.meta"),
		[]byte(`{"ETag":"\"x\"","Content-Type":"text/plain","X-Amz-Meta-Team":"core"}`), 0644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/src.txt", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<CopyObjectResult>") || !strings.Contains(w.Body.String(), "<ETag>") {
		t.Fatalf("expected CopyObjectResult with ETag: %s", w.Body.String())
	}
	// Source survives.
	if _, err := os.Stat(filepath.Join(dir, "b1", "src.txt")); err != nil {
		t.Fatalf("source must survive copy: %v", err)
	}
	// Destination bytes match source.
	got, err := os.ReadFile(filepath.Join(dir, "b1", "dst.txt"))
	if err != nil || string(got) != "hello world" {
		t.Fatalf("dst bytes=%q err=%v", got, err)
	}
}

func TestCopyObject_DirectiveCopyCarriesSourceMetadata(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "src.txt", "data")
	if err := os.WriteFile(filepath.Join(dir, "b1", "src.txt.meta"),
		[]byte(`{"ETag":"\"x\"","Content-Type":"application/pdf","X-Amz-Meta-Team":"core"}`), 0644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/src.txt", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	m := readMeta(t, dir, "b1", "dst.txt")
	if m["Content-Type"] != "application/pdf" {
		t.Fatalf("content-type not carried: %q", m["Content-Type"])
	}
	if m["X-Amz-Meta-Team"] != "core" {
		t.Fatalf("user metadata not carried: %q", m["X-Amz-Meta-Team"])
	}
}

func TestCopyObject_DirectiveReplaceUsesRequestMetadata(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "src.txt", "data")
	if err := os.WriteFile(filepath.Join(dir, "b1", "src.txt.meta"),
		[]byte(`{"ETag":"\"x\"","Content-Type":"application/pdf","X-Amz-Meta-Team":"core"}`), 0644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/src.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "text/csv",
		"x-amz-meta-owner":         "ops",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	m := readMeta(t, dir, "b1", "dst.txt")
	if m["Content-Type"] != "text/csv" {
		t.Fatalf("replace must use request content-type, got %q", m["Content-Type"])
	}
	if m["X-Amz-Meta-Owner"] != "ops" {
		t.Fatalf("replace must use request metadata, got %q", m["X-Amz-Meta-Owner"])
	}
	if _, ok := m["X-Amz-Meta-Team"]; ok {
		t.Fatalf("replace must drop source metadata, found team=%q", m["X-Amz-Meta-Team"])
	}
}

func TestCopyObject_MissingSourceIs404(t *testing.T) {
	seedACLBucket(t, "b1")
	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/ghost.txt", nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "NoSuchKey") {
		t.Fatalf("expected 404 NoSuchKey, got %d body=%s", w.Code, w.Body.String())
	}
}

// Security: a traversal copy-source must be rejected and must not create the
// destination (which would otherwise leak arbitrary file contents).
func TestCopyObject_RejectsTraversalSource(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/../../etc/passwd", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal source, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "b1", "dst.txt")); !os.IsNotExist(err) {
		t.Fatalf("destination must not be created on rejected source")
	}
}

func TestCopyObject_RejectsSidecarSource(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	if err := os.WriteFile(filepath.Join(dir, "b1", ".acl.json"), []byte(`{"canned":"private"}`), 0644); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/.acl.json", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for sidecar source, got %d", w.Code)
	}
}

func TestCopyObject_SelfCopyRequiresReplace(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "obj.txt", "data")
	if err := os.WriteFile(filepath.Join(dir, "b1", "obj.txt.meta"),
		[]byte(`{"ETag":"\"x\"","Content-Type":"text/plain"}`), 0644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	// COPY onto itself with no change is illegal, matching AWS.
	w := copyReq(t, newCopyEngine(), "b1", "obj.txt", "/b1/obj.txt", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("self-copy with COPY must be 400, got %d", w.Code)
	}

	// REPLACE onto itself is a legal metadata update.
	w = copyReq(t, newCopyEngine(), "b1", "obj.txt", "/b1/obj.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "text/markdown",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("self-copy with REPLACE must succeed, got %d body=%s", w.Code, w.Body.String())
	}
	if m := readMeta(t, dir, "b1", "obj.txt"); m["Content-Type"] != "text/markdown" {
		t.Fatalf("self-replace did not update content-type: %q", m["Content-Type"])
	}
}

func TestCopyObject_CannedACLAppliesToDestination(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "src.txt", "data")
	if err := os.WriteFile(filepath.Join(dir, "b1", "src.txt.meta"),
		[]byte(`{"ETag":"\"x\"","Content-Type":"text/plain"}`), 0644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	w := copyReq(t, newCopyEngine(), "b1", "dst.txt", "/b1/src.txt", map[string]string{
		"x-amz-acl": "public-read",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	acl, src, err := storage.ResolveObjectACL("b1", filepath.Join(dir, "b1", "dst.txt"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if acl != storage.ACLPublicRead || src != storage.ACLSourceObject {
		t.Fatalf("dst acl=%q src=%q want public-read/object", acl, src)
	}
}
