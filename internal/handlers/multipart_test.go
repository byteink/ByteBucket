package handlers

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withMultipartRoots synchronises the handler-local objectsRoot with both
// storage.ObjectsRoot and storage.UploadsRoot so the handler and storage
// layers look at the same tree. Production defaults keep these aligned;
// tests must do it explicitly or Complete writes to the wrong dir.
func withMultipartRoots(t *testing.T) (string, string) {
	t.Helper()
	objDir := t.TempDir()
	upDir := t.TempDir()
	origHandlerRoot := objectsRoot
	origObjRoot := storage.ObjectsRoot
	origUpRoot := storage.UploadsRoot
	objectsRoot = objDir
	storage.ObjectsRoot = objDir
	storage.UploadsRoot = upDir
	t.Cleanup(func() {
		objectsRoot = origHandlerRoot
		storage.ObjectsRoot = origObjRoot
		storage.UploadsRoot = origUpRoot
	})
	return objDir, upDir
}

// TestMultipartHandlers_Roundtrip drives all six endpoints in-process and
// asserts the composite ETag exactly matches hex(md5(concat(md5s))) + "-N".
// If this test drifts, real SDKs drift with it.
func TestMultipartHandlers_Roundtrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	objDir, _ := withMultipartRoots(t)
	bucket, key := "bkt", "big.bin"
	if err := os.MkdirAll(filepath.Join(objDir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 1. Create
	crW := httptest.NewRecorder()
	crC, _ := gin.CreateTestContext(crW)
	crC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	crC.Request = httptest.NewRequest(http.MethodPost, "/"+bucket+"/"+key+"?uploads", nil)
	crC.Request.Header.Set("x-amz-meta-author", "test")
	CreateMultipartUploadHandler(crC)
	if crW.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", crW.Code, crW.Body.String())
	}
	var initResult struct {
		Bucket   string `xml:"Bucket"`
		Key      string `xml:"Key"`
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(crW.Body.Bytes(), &initResult); err != nil {
		t.Fatalf("xml parse: %v body=%s", err, crW.Body.String())
	}
	if initResult.UploadID == "" {
		t.Fatalf("empty upload id: %s", crW.Body.String())
	}

	// 2. Upload three parts
	parts := [][]byte{
		bytes.Repeat([]byte("x"), 2048),
		bytes.Repeat([]byte("y"), 1024),
		bytes.Repeat([]byte("z"), 512),
	}
	etags := make([]string, len(parts))
	for i, body := range parts {
		pn := i + 1
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
		c.Request = httptest.NewRequest(http.MethodPut,
			fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s", bucket, key, pn, initResult.UploadID),
			bytes.NewReader(body))
		UploadPartHandler(c)
		if w.Code != http.StatusOK {
			t.Fatalf("upload part %d status=%d body=%s", pn, w.Code, w.Body.String())
		}
		etags[i] = w.Header().Get("ETag")
		sum := md5.Sum(body)
		wantETag := "\"" + hex.EncodeToString(sum[:]) + "\""
		if etags[i] != wantETag {
			t.Fatalf("part %d etag=%q want %q", pn, etags[i], wantETag)
		}
	}

	// 3. ListParts
	lpW := httptest.NewRecorder()
	lpC, _ := gin.CreateTestContext(lpW)
	lpC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	lpC.Request = httptest.NewRequest(http.MethodGet,
		"/"+bucket+"/"+key+"?uploadId="+initResult.UploadID, nil)
	ListPartsHandler(lpC)
	if lpW.Code != http.StatusOK {
		t.Fatalf("listparts status=%d body=%s", lpW.Code, lpW.Body.String())
	}
	if !strings.Contains(lpW.Body.String(), "<Part>") {
		t.Fatalf("listparts body missing <Part>: %s", lpW.Body.String())
	}

	// 4. Complete
	var reqBody bytes.Buffer
	reqBody.WriteString("<CompleteMultipartUpload>")
	for i, et := range etags {
		fmt.Fprintf(&reqBody, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, et)
	}
	reqBody.WriteString("</CompleteMultipartUpload>")
	cpW := httptest.NewRecorder()
	cpC, _ := gin.CreateTestContext(cpW)
	cpC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	cpC.Request = httptest.NewRequest(http.MethodPost,
		"/"+bucket+"/"+key+"?uploadId="+initResult.UploadID, &reqBody)
	CompleteMultipartUploadHandler(cpC)
	if cpW.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", cpW.Code, cpW.Body.String())
	}

	// Composite ETag assertion: hex(md5(concat(md5_of_each_part))) + "-N"
	h := md5.New()
	for _, p := range parts {
		sum := md5.Sum(p)
		h.Write(sum[:])
	}
	wantFinal := fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(h.Sum(nil)), len(parts))
	if got := cpW.Header().Get("ETag"); got != wantFinal {
		t.Fatalf("complete header ETag=%q want %q body=%s", got, wantFinal, cpW.Body.String())
	}
	// Final object bytes
	finalPath := filepath.Join(objDir, bucket, key)
	concat, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	want := append(append([]byte{}, parts[0]...), append(parts[1], parts[2]...)...)
	if !bytes.Equal(concat, want) {
		t.Fatalf("final bytes mismatch: got %d want %d", len(concat), len(want))
	}
}

func TestMultipartHandlers_AbortThenList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	objDir, _ := withMultipartRoots(t)
	bucket, key := "bkt", "abort.bin"
	if err := os.MkdirAll(filepath.Join(objDir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	up, err := storage.CreateMultipartUpload(bucket, key, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := storage.UploadPart(bucket, key, up.UploadID, 1, bytes.NewReader([]byte("hi"))); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Abort via a real engine so c.Status(204) is actually flushed to the
	// recorder; CreateTestContext leaves a deferred writer that only resolves
	// on body writes.
	eng := gin.New()
	eng.DELETE("/:bucket/*objectKey", AbortMultipartUploadHandler)
	abW := httptest.NewRecorder()
	abReq := httptest.NewRequest(http.MethodDelete,
		"/"+bucket+"/"+key+"?uploadId="+up.UploadID, nil)
	eng.ServeHTTP(abW, abReq)
	if abW.Code != http.StatusNoContent {
		t.Fatalf("abort status=%d body=%s", abW.Code, abW.Body.String())
	}

	// ListMultipartUploads should return no entries
	lsW := httptest.NewRecorder()
	lsC, _ := gin.CreateTestContext(lsW)
	lsC.Params = gin.Params{{Key: "bucket", Value: bucket}}
	lsC.Request = httptest.NewRequest(http.MethodGet, "/"+bucket+"?uploads", nil)
	ListMultipartUploadsHandler(lsC)
	if lsW.Code != http.StatusOK {
		t.Fatalf("list uploads status=%d", lsW.Code)
	}
	if strings.Contains(lsW.Body.String(), "<Upload>") {
		t.Fatalf("expected empty uploads, got: %s", lsW.Body.String())
	}
}

func TestMultipartHandlers_NoSuchUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withMultipartRoots(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: "bkt"}, {Key: "objectKey", Value: "/k"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/bkt/k?uploadId=missing", nil)
	AbortMultipartUploadHandler(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("abort status=%d body=%s; want 404", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "NoSuchUpload") {
		t.Fatalf("body missing NoSuchUpload: %s", w.Body.String())
	}
}
