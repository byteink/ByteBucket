package handlers

import (
	"bytes"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withContainedRoots points both object roots at <tmp>/objects and plants an
// object at <tmp>/x, one level above them. A handler that joins an unchecked
// ".." bucket under the root lands on that object.
func withContainedRoots(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "objects")
	for _, dir := range []string{filepath.Join(root, "src"), root} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(parent, "x"):      "outside",
		filepath.Join(parent, "x.meta"): `{"ETag":"\"e\""}`,
		filepath.Join(root, "src", "k"): "source",
	} {
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	origStore, origHandler := storage.ObjectsRoot, objectsRoot
	storage.ObjectsRoot, objectsRoot = root, root
	t.Cleanup(func() {
		storage.ObjectsRoot, objectsRoot = origStore, origHandler
	})
	return parent
}

// Handlers are called directly, bypassing ValidateNames, so this proves the
// filesystem layer holds on its own if the middleware is ever skipped.
func TestHandlersRejectPathEscape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name    string
		method  string
		handler gin.HandlerFunc
		key     string
		header  map[string]string
		status  int
		message string
	}{
		{"PutBucketACL", http.MethodPut, PutBucketACLHandler, "", map[string]string{"x-amz-acl": "public-read"}, http.StatusBadRequest, msgPathEscape},
		{"GetBucketACL", http.MethodGet, GetBucketACLHandler, "", nil, http.StatusBadRequest, msgPathEscape},
		{"PutObjectACL", http.MethodPut, PutObjectACLHandler, "/x", map[string]string{"x-amz-acl": "public-read"}, http.StatusBadRequest, msgPathEscape},
		{"GetObjectACL", http.MethodGet, GetObjectACLHandler, "/x", nil, http.StatusBadRequest, msgPathEscape},
		{"GetObjectTagging", http.MethodGet, GetObjectTaggingHandler, "/x", nil, http.StatusBadRequest, msgPathEscape},
		{"DeleteBucket", http.MethodDelete, DeleteBucketHandler, "", nil, http.StatusBadRequest, msgPathEscape},
		{"ListObjects", http.MethodGet, ListObjectsHandler, "", nil, http.StatusBadRequest, msgPathEscape},
		{"HeadBucket", http.MethodHead, HeadBucketHandler, "", nil, http.StatusBadRequest, ""},
		{"CopyObject", http.MethodPut, CopyObjectHandler, "/x", map[string]string{"x-amz-copy-source": "/src/k"}, http.StatusBadRequest, msgPathEscape},
		{"UploadObject", http.MethodPut, UploadObjectHandler, "/x", nil, http.StatusBadRequest, msgPathEscape},
		{"DownloadObject", http.MethodGet, DownloadObjectHandler, "/x", nil, http.StatusBadRequest, msgPathEscape},
		{"GetObjectMetadata", http.MethodGet, GetObjectMetadataHandler, "/x", nil, http.StatusBadRequest, msgPathEscape},
		{"DeleteObject", http.MethodDelete, DeleteObjectHandler, "/x", nil, http.StatusInternalServerError, "Error deleting object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := withContainedRoots(t)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tc.method, "/../x", bytes.NewReader([]byte("overwrite")))
			for k, v := range tc.header {
				c.Request.Header.Set(k, v)
			}
			c.Params = gin.Params{{Key: "bucket", Value: ".."}, {Key: "objectKey", Value: tc.key}}

			tc.handler(c)

			// c.Writer, not the recorder: a bare c.Status is only flushed by
			// the engine, which a direct handler call skips.
			if got := c.Writer.Status(); got != tc.status {
				t.Fatalf("status=%d want %d body=%s", got, tc.status, w.Body.String())
			}
			if tc.message != "" {
				var body S3ErrorBody
				if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Message != tc.message {
					t.Fatalf("message=%q err=%v want %q", body.Message, err, tc.message)
				}
			}
			if data, err := os.ReadFile(filepath.Join(parent, "x")); err != nil || string(data) != "outside" {
				t.Fatalf("object outside root touched: %q %v", data, err)
			}
			for _, name := range []string{".acl.json", "x.tags.json"} {
				if _, err := os.Stat(filepath.Join(parent, name)); !os.IsNotExist(err) {
					t.Fatalf("%s written outside root: %v", name, err)
				}
			}
		})
	}
}

// removeObject is also reached from DeleteObjects with a key that has passed
// ValidateObjectKey, so it must contain the key against its own bucket rather
// than trust the caller.
func TestRemoveObjectRejectsKeyEscape(t *testing.T) {
	parent := withContainedRoots(t)
	if err := removeObject("src", "../../x"); !errors.Is(err, storage.ErrPathEscape) {
		t.Fatalf("err=%v want ErrPathEscape", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "x")); err != nil {
		t.Fatalf("object outside bucket removed: %v", err)
	}
}
