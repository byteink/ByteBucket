package handlers

import (
	"bytes"
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

// newACLTestEngine wires the bucket and object ACL handlers behind the same
// query-subresource dispatch the production router uses. No auth middleware
// is attached: the handlers themselves carry the security contract and are
// the unit under test here.
func newACLTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["acl"]; ok {
			PutBucketACLHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.GET("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["acl"]; ok {
			GetBucketACLHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.PUT("/:bucket/*objectKey", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["acl"]; ok {
			PutObjectACLHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.GET("/:bucket/*objectKey", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["acl"]; ok {
			GetObjectACLHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

// seedACLBucket points ObjectsRoot at a temp dir, creates the named bucket,
// and synchronises the handler-level objectsRoot so the two stay aligned.
func seedACLBucket(t *testing.T, bucket string) string {
	t.Helper()
	dir := t.TempDir()
	origStore, origHandler := storage.ObjectsRoot, objectsRoot
	storage.ObjectsRoot = dir
	objectsRoot = dir
	t.Cleanup(func() {
		storage.ObjectsRoot = origStore
		objectsRoot = origHandler
	})
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

func TestPutBucketACL_CannedHeaderJSON(t *testing.T) {
	dir := seedACLBucket(t, "b1")

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1?acl", nil)
	req.Header.Set("x-amz-acl", "public-read")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	// Sidecar must be written and decode back to public-read.
	raw, err := os.ReadFile(filepath.Join(dir, "b1", ".acl.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var acl storage.BucketACL
	if err := json.Unmarshal(raw, &acl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if acl.Canned != storage.ACLPublicRead {
		t.Fatalf("canned=%q want %q", acl.Canned, storage.ACLPublicRead)
	}
}

func TestPutBucketACL_XMLGrant(t *testing.T) {
	seedACLBucket(t, "b1")

	body := []byte(`<AccessControlPolicy>
  <Owner><ID>owner</ID></Owner>
  <AccessControlList>
    <Grant>
      <Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group">
        <URI>http://acs.amazonaws.com/groups/global/AllUsers</URI>
      </Grantee>
      <Permission>READ</Permission>
    </Grant>
  </AccessControlList>
</AccessControlPolicy>`)

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1?acl", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	got, err := storage.EffectiveBucketACL("b1")
	if err != nil || got != storage.ACLPublicRead {
		t.Fatalf("effective=%q err=%v", got, err)
	}
}

func TestPutBucketACL_RejectsUnsupportedCanned(t *testing.T) {
	seedACLBucket(t, "b1")

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1?acl", nil)
	req.Header.Set("x-amz-acl", "public-read-write")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetBucketACL_DefaultPrivate(t *testing.T) {
	seedACLBucket(t, "b1")

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/b1?acl", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	var body struct {
		Canned string `json:"canned"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Canned != storage.ACLPrivate {
		t.Fatalf("canned=%q want private", body.Canned)
	}
}

func TestPutObjectACL_PersistsAndResolves(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	objPath := filepath.Join(dir, "b1", "k")
	if err := os.WriteFile(objPath, []byte("body"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1/k?acl", nil)
	req.Header.Set("x-amz-acl", "public-read")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	got, src, err := storage.ResolveObjectACL("b1", objPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != storage.ACLPublicRead || src != storage.ACLSourceObject {
		t.Fatalf("acl=%q src=%q", got, src)
	}
}

func TestPutObjectACL_NoSuchKey(t *testing.T) {
	seedACLBucket(t, "b1")

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodPut, "/b1/missing?acl", nil)
	req.Header.Set("x-amz-acl", "public-read")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "NoSuchKey") {
		t.Fatalf("expected NoSuchKey, got %s", w.Body.String())
	}
}

func TestGetObjectACL_DefaultsPrivateThenReflectsOverride(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	if err := os.WriteFile(filepath.Join(dir, "b1", "k"), []byte("body"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	r := newACLTestEngine()

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/b1/k?acl", nil)
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get: %d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Canned string `json:"canned"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return body.Canned
	}

	if got := get(); got != storage.ACLPrivate {
		t.Fatalf("default canned=%q want private", got)
	}

	if err := storage.SetObjectACL(filepath.Join(dir, "b1", "k"), storage.ACLPublicRead); err != nil {
		t.Fatalf("set object acl: %v", err)
	}
	if got := get(); got != storage.ACLPublicRead {
		t.Fatalf("after override canned=%q want public-read", got)
	}
}

func TestGetObjectACL_NoSuchKey(t *testing.T) {
	seedACLBucket(t, "b1")

	r := newACLTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/b1/missing?acl", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "NoSuchKey") {
		t.Fatalf("expected NoSuchKey, got %s", w.Body.String())
	}
}

func TestUploadObjectHonoursCannedACLHeader(t *testing.T) {
	dir := seedACLBucket(t, "b1")

	// Drive UploadObjectHandler directly so we exercise the canned-ACL
	// branch without re-creating SigV4 plumbing in the test.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/b1/file.txt", bytes.NewReader([]byte("hi")))
	c.Request.Header.Set("x-amz-acl", "public-read")
	c.Params = gin.Params{
		{Key: "bucket", Value: "b1"},
		{Key: "objectKey", Value: "/file.txt"},
	}
	UploadObjectHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d body=%s", w.Code, w.Body.String())
	}

	acl, src, err := storage.ResolveObjectACL("b1", filepath.Join(dir, "b1", "file.txt"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if acl != storage.ACLPublicRead || src != storage.ACLSourceObject {
		t.Fatalf("acl=%q src=%q want public-read/object", acl, src)
	}
}

func TestCreateBucketHonoursCannedACLHeader(t *testing.T) {
	seedACLBucket(t, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/b1", nil)
	c.Request.Header.Set("x-amz-acl", "public-read")
	c.Params = gin.Params{{Key: "bucket", Value: "b1"}}
	CreateBucketHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}
	got, err := storage.EffectiveBucketACL("b1")
	if err != nil || got != storage.ACLPublicRead {
		t.Fatalf("effective=%q err=%v", got, err)
	}
}
