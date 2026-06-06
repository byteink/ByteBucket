package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func getObjectWithHeaders(t *testing.T, bucket, key string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	req := httptest.NewRequest(http.MethodGet, "/"+bucket+"/"+key, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	DownloadObjectHandler(c)
	return w
}

// GET conditionals are delegated to http.ServeContent, which evaluates
// If-None-Match / If-Match against the ETag header we set. These tests lock
// that contract so a future refactor of the download path cannot silently
// drop precondition handling.
func TestConditionalGet_IfNoneMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	body := []byte("conditional body")
	seedObject(t, "condbkt", "obj.txt", body)
	etag := expectedETag(body)

	if w := getObjectWithHeaders(t, "condbkt", "obj.txt", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match with current ETag must be 304, got %d", w.Code)
	}
	if w := getObjectWithHeaders(t, "condbkt", "obj.txt", map[string]string{"If-None-Match": `"deadbeef"`}); w.Code != http.StatusOK {
		t.Fatalf("If-None-Match with stale ETag must be 200, got %d", w.Code)
	}
}

func TestConditionalGet_IfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	body := []byte("conditional body")
	seedObject(t, "condbkt", "obj.txt", body)
	etag := expectedETag(body)

	if w := getObjectWithHeaders(t, "condbkt", "obj.txt", map[string]string{"If-Match": etag}); w.Code != http.StatusOK {
		t.Fatalf("If-Match with current ETag must be 200, got %d", w.Code)
	}
	if w := getObjectWithHeaders(t, "condbkt", "obj.txt", map[string]string{"If-Match": `"deadbeef"`}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match with stale ETag must be 412, got %d", w.Code)
	}
}

func putObjectWithHeaders(t *testing.T, bucket, key string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	req := httptest.NewRequest(http.MethodPut, "/"+bucket+"/"+key, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	UploadObjectHandler(c)
	return w
}

func TestConditionalPut_IfNoneMatchStar(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	// First create with If-None-Match: * succeeds (object absent).
	if w := putObjectWithHeaders(t, "cbkt", "k.txt", []byte("v1"), map[string]string{"If-None-Match": "*"}); w.Code != http.StatusOK {
		t.Fatalf("create with If-None-Match:* must be 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Second create with If-None-Match: * must fail (object now exists).
	if w := putObjectWithHeaders(t, "cbkt", "k.txt", []byte("v2"), map[string]string{"If-None-Match": "*"}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("overwrite with If-None-Match:* must be 412, got %d", w.Code)
	}
}

func TestConditionalPut_IfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	seedObject(t, "cbkt", "k.txt", []byte("v1"))
	etag := expectedETag([]byte("v1"))

	// If-Match with current ETag overwrites.
	if w := putObjectWithHeaders(t, "cbkt", "k.txt", []byte("v2"), map[string]string{"If-Match": etag}); w.Code != http.StatusOK {
		t.Fatalf("If-Match with current ETag must be 200, got %d", w.Code)
	}
	// If-Match with a stale ETag is rejected (object changed under us).
	if w := putObjectWithHeaders(t, "cbkt", "k.txt", []byte("v3"), map[string]string{"If-Match": etag}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match with stale ETag must be 412, got %d", w.Code)
	}
	// If-Match on a missing object is rejected.
	if w := putObjectWithHeaders(t, "cbkt", "missing.txt", []byte("x"), map[string]string{"If-Match": etag}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on absent object must be 412, got %d", w.Code)
	}
}
