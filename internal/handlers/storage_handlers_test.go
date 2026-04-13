package handlers

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withTempObjectsRoot redirects the package-level objectsRoot at a temp dir
// for the life of the test, so handler tests do not touch /data.
func withTempObjectsRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := objectsRoot
	objectsRoot = dir
	t.Cleanup(func() { objectsRoot = orig })
	return dir
}

// ListBucketsHandler must echo the authenticated caller as Owner. Hardcoding
// a placeholder is user-visible via any S3 SDK's ListBuckets response, and
// breaks downstream tooling that gates on the owner identity.
func TestListBucketsHandlerUsesAuthenticatedOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := withTempObjectsRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set("user", &storage.User{AccessKeyID: "AKIAEXAMPLE"})

	ListBucketsHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if strings.Contains(body, "dummy-owner") || strings.Contains(body, "dummy-owner-id") {
		t.Fatalf("response still contains placeholder owner: %s", body)
	}

	var parsed struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("xml parse: %v; body=%s", err, body)
	}
	if parsed.Owner.ID != "AKIAEXAMPLE" || parsed.Owner.DisplayName != "AKIAEXAMPLE" {
		t.Fatalf("owner = {%q,%q}; want AKIAEXAMPLE in both",
			parsed.Owner.ID, parsed.Owner.DisplayName)
	}
}

// expectedETag returns the S3-wire-format ETag for a byte slice: the hex md5
// wrapped in literal double quotes. Tests pin this format because SDKs
// pattern-match it verbatim.
func expectedETag(body []byte) string {
	sum := md5.Sum(body)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

// putObject runs UploadObjectHandler in-process against the given bucket/key,
// returning the recorded response for header/status assertions.
func putObject(t *testing.T, bucket, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	c.Request = httptest.NewRequest(http.MethodPut, "/"+bucket+"/"+key, bytes.NewReader(body))
	UploadObjectHandler(c)
	return w
}

// The ETag returned by PUT, GET, HEAD and LIST must all agree and must be the
// hex md5 of the uploaded bytes, wrapped in double quotes. A drift between
// any of these paths would break SDKs that verify object integrity via ETag.
func TestETagConsistencyAcrossPutGetHeadList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := withTempObjectsRoot(t)
	bucket := "bkt"
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("the payload we signed for")
	want := expectedETag(body)

	putW := putObject(t, bucket, "obj.bin", body)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", putW.Code, putW.Body.String())
	}
	if got := putW.Header().Get("ETag"); got != want {
		t.Fatalf("PUT ETag = %q; want %q", got, want)
	}

	// GET
	getW := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getW)
	getC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/obj.bin"}}
	getC.Request = httptest.NewRequest(http.MethodGet, "/"+bucket+"/obj.bin", nil)
	DownloadObjectHandler(getC)
	if got := getW.Header().Get("ETag"); got != want {
		t.Fatalf("GET ETag = %q; want %q", got, want)
	}

	// HEAD via metadata handler
	headW := httptest.NewRecorder()
	headC, _ := gin.CreateTestContext(headW)
	headC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/obj.bin"}}
	headC.Request = httptest.NewRequest(http.MethodHead, "/"+bucket+"/obj.bin", nil)
	GetObjectMetadataHandler(headC)
	if got := headW.Header().Get("ETag"); got != want {
		t.Fatalf("HEAD ETag = %q; want %q", got, want)
	}

	// LIST — parse the XML rather than string-match so the comparison is not
	// fooled by encoding choices (encoding/xml escapes quotes as &#34;).
	listW := httptest.NewRecorder()
	listC, _ := gin.CreateTestContext(listW)
	listC.Params = gin.Params{{Key: "bucket", Value: bucket}}
	listC.Request = httptest.NewRequest(http.MethodGet, "/"+bucket, nil)
	ListObjectsHandler(listC)
	var lbr struct {
		Contents []struct {
			Key  string `xml:"Key"`
			ETag string `xml:"ETag"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(listW.Body.Bytes(), &lbr); err != nil {
		t.Fatalf("LIST xml parse: %v; body=%s", err, listW.Body.String())
	}
	if len(lbr.Contents) != 1 || lbr.Contents[0].ETag != want {
		t.Fatalf("LIST ETag = %+v; want one entry with %q", lbr.Contents, want)
	}
}

// Legacy objects written before ETag persistence have no sidecar entry for
// ETag. The first read must backfill the value from the file on disk and
// persist it, matching what a fresh PUT would have written.
func TestETagBackfillForLegacyObjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := withTempObjectsRoot(t)
	bucket := "legacy"
	bucketDir := filepath.Join(dir, bucket)
	if err := os.MkdirAll(bucketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	body := []byte("legacy bytes")
	objPath := filepath.Join(bucketDir, "obj")
	if err := os.WriteFile(objPath, body, 0644); err != nil {
		t.Fatalf("write obj: %v", err)
	}
	// Sidecar without an ETag field, mimicking pre-migration metadata.
	if err := os.WriteFile(objPath+".meta", []byte(`{"x-amz-meta-foo":"bar"}`), 0644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	want := expectedETag(body)

	getW := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getW)
	getC.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/obj"}}
	getC.Request = httptest.NewRequest(http.MethodGet, "/"+bucket+"/obj", nil)
	DownloadObjectHandler(getC)
	if got := getW.Header().Get("ETag"); got != want {
		t.Fatalf("legacy GET ETag = %q; want %q", got, want)
	}

	// The backfill must have persisted; parsing the sidecar directly should
	// now yield the ETag so the next request avoids the hash.
	raw, err := os.ReadFile(objPath + ".meta")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var reloaded map[string]string
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("parse sidecar: %v; raw=%s", err, raw)
	}
	if reloaded["ETag"] != want {
		t.Fatalf("sidecar ETag = %q; want %q", reloaded["ETag"], want)
	}
}
