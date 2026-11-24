package handlers

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type competingUploadReader struct {
	t        *testing.T
	bucket   string
	key      string
	headers  map[string]string
	body     *strings.Reader
	competed bool
}

func (r *competingUploadReader) Read(p []byte) (int, error) {
	if !r.competed {
		r.competed = true
		// Fail instead of deadlocking if an upload still holds its stripe
		// while a slow body prevents a competing request from committing.
		h := fnv.New32a()
		if _, err := h.Write([]byte(filepath.Join(objectsRoot, r.bucket, r.key))); err != nil {
			return 0, err
		}
		stripe := &objectLocks[h.Sum32()%objectLockStripes]
		if !stripe.TryLock() {
			return 0, fmt.Errorf("object stripe held while reading request body")
		}
		stripe.Unlock()
		response := putObjectWithHeaders(r.t, r.bucket, r.key, []byte("winner"), r.headers)
		if response.Code != http.StatusOK {
			return 0, fmt.Errorf("competing upload returned %d: %s", response.Code, response.Body.String())
		}
	}
	return r.body.Read(p)
}

func TestConditionalPut_ConcurrentCreateOnly(t *testing.T) {
	withTempObjectsRoot(t)
	assertCompetingConditionalUpload(t, map[string]string{"If-None-Match": "*"})
}

func TestConditionalPut_ConcurrentIfMatch(t *testing.T) {
	withTempObjectsRoot(t)
	seedObject(t, "cbkt", "k.txt", []byte("original"))
	assertCompetingConditionalUpload(t, map[string]string{"If-Match": expectedETag([]byte("original"))})
}

func assertCompetingConditionalUpload(t *testing.T, headers map[string]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := &competingUploadReader{
		t: t, bucket: "cbkt", key: "k.txt", headers: headers,
		body: strings.NewReader("loser"),
	}
	response := putObjectWithReader(t, "cbkt", "k.txt", body, headers)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("late conditional upload = %d, want 412: %s", response.Code, response.Body.String())
	}
	if !body.competed {
		t.Fatal("competing write did not run while the first body was being staged")
	}
	path := filepath.Join(objectsRoot, "cbkt", "k.txt")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "winner" {
		t.Fatalf("winner bytes = %q, error = %v", got, err)
	}
	assertNoStagedObjects(t, filepath.Dir(path))
}

func putObjectWithReader(t *testing.T, bucket, key string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	c.Request = httptest.NewRequest(http.MethodPut, "/"+bucket+"/"+key, body)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	UploadObjectHandler(c)
	return response
}

func assertNoStagedObjects(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("staged object files left behind: %v", paths)
	}
}
