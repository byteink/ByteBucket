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
)

// seedDeleteBucket sets the ObjectsRoot pair to a temp dir, creates the
// named bucket, and returns the absolute bucket path so callers can
// drop files into it before invoking the delete handler.
func seedDeleteBucket(t *testing.T, bucket string) string {
	t.Helper()
	dir := t.TempDir()
	origStore, origHandler := storage.ObjectsRoot, objectsRoot
	storage.ObjectsRoot = dir
	objectsRoot = dir
	t.Cleanup(func() {
		storage.ObjectsRoot = origStore
		objectsRoot = origHandler
	})
	bp := filepath.Join(dir, bucket)
	if err := os.MkdirAll(bp, 0755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	return bp
}

// callDelete drives DeleteBucketHandler through a real Gin engine because
// c.Status() only sets the recorded status; the underlying flush happens
// when the engine finalises the response. Routing via Engine.ServeHTTP is
// the only way to see the correct status code in the test recorder.
func callDelete(t *testing.T, bucket string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.DELETE("/api/s3/:bucket", DeleteBucketHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/s3/"+bucket, nil))
	return w
}

func TestDeleteBucket_EmptyBucket204(t *testing.T) {
	seedDeleteBucket(t, "empty")
	w := callDelete(t, "empty")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteBucket_NotEmptyConflict(t *testing.T) {
	bp := seedDeleteBucket(t, "full")
	if err := os.WriteFile(filepath.Join(bp, "file.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	w := callDelete(t, "full")
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BucketNotEmpty") {
		t.Fatalf("expected BucketNotEmpty code, got %s", w.Body.String())
	}
	// File must survive the failed delete.
	if _, err := os.Stat(filepath.Join(bp, "file.txt")); err != nil {
		t.Fatalf("file was removed by failed delete: %v", err)
	}
}

// A bucket carrying only the per-bucket sidecars (CORS/ACL set, then all
// real objects removed) is logically empty. The delete must succeed and
// take the sidecars with it. This is the case a user hits via the UI:
// they delete all photos, then delete the bucket.
func TestDeleteBucket_OnlySidecarsCountsAsEmpty(t *testing.T) {
	bp := seedDeleteBucket(t, "sidecars")
	for _, name := range []string{".cors.json", ".acl.json"} {
		if err := os.WriteFile(filepath.Join(bp, name), []byte("{}"), 0644); err != nil {
			t.Fatalf("write sidecar %s: %v", name, err)
		}
	}
	w := callDelete(t, "sidecars")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(bp); !os.IsNotExist(err) {
		t.Fatalf("bucket dir still exists after delete")
	}
}

func TestDeleteBucket_NestedKeysBlockDelete(t *testing.T) {
	bp := seedDeleteBucket(t, "nested")
	deep := filepath.Join(bp, "a", "b", "c", "x.txt")
	if err := os.MkdirAll(filepath.Dir(deep), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(deep, []byte("y"), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	w := callDelete(t, "nested")
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409, body=%s", w.Code, w.Body.String())
	}
}
