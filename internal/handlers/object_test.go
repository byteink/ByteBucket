package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// getObjectWithRange drives DownloadObjectHandler in-process for the given
// bucket/key, optionally attaching a Range header. An empty rangeHeader sends
// no Range at all so the full-body path is exercised.
func getObjectWithRange(t *testing.T, bucket, key, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	req := httptest.NewRequest(http.MethodGet, "/"+bucket+"/"+key, nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	c.Request = req
	DownloadObjectHandler(c)
	return w
}

// seedObject writes an object plus its ETag-bearing sidecar through the upload
// handler so the GET path sees a realistic on-disk shape (sidecar Content-Type
// and Content-Length present), then returns the bucket dir.
func seedObject(t *testing.T, bucket, key string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(objectsRoot, bucket), 0755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if w := putObject(t, bucket, key, body); w.Code != http.StatusOK {
		t.Fatalf("seed PUT status = %d; body=%s", w.Code, w.Body.String())
	}
}

// Range support on GetObject must follow RFC 7233: a satisfiable single range
// yields 206 with a precise Content-Range/Content-Length and only the slice
// bytes; an unsatisfiable range yields 416 with "bytes */total"; a malformed
// Range header is ignored and the full 200 body is served. Every response —
// including the no-Range 200 — must advertise Accept-Ranges so clients can
// resume. The ETag must never change with ranging.
func TestDownloadObjectRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	bucket := "rangebkt"
	key := "obj.bin"
	body := []byte("0123456789") // 10 bytes, indices 0..9
	seedObject(t, bucket, key, body)
	wantETag := expectedETag(body)

	cases := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantBody    string
		wantCRange  string // expected Content-Range header ("" = must be absent)
		wantCLen    string // expected Content-Length header
	}{
		{
			name:        "no range full body",
			rangeHeader: "",
			wantStatus:  http.StatusOK,
			wantBody:    "0123456789",
			wantCRange:  "",
			wantCLen:    "10",
		},
		{
			name:        "prefix bytes 0-3",
			rangeHeader: "bytes=0-3",
			wantStatus:  http.StatusPartialContent,
			wantBody:    "0123",
			wantCRange:  "bytes 0-3/10",
			wantCLen:    "4",
		},
		{
			name:        "mid range bytes 2-5",
			rangeHeader: "bytes=2-5",
			wantStatus:  http.StatusPartialContent,
			wantBody:    "2345",
			wantCRange:  "bytes 2-5/10",
			wantCLen:    "4",
		},
		{
			name:        "open ended bytes 5-",
			rangeHeader: "bytes=5-",
			wantStatus:  http.StatusPartialContent,
			wantBody:    "56789",
			wantCRange:  "bytes 5-9/10",
			wantCLen:    "5",
		},
		{
			name:        "suffix last 4 bytes",
			rangeHeader: "bytes=-4",
			wantStatus:  http.StatusPartialContent,
			wantBody:    "6789",
			wantCRange:  "bytes 6-9/10",
			wantCLen:    "4",
		},
		{
			name:        "end past size clamps to last byte",
			rangeHeader: "bytes=8-99",
			wantStatus:  http.StatusPartialContent,
			wantBody:    "89",
			wantCRange:  "bytes 8-9/10",
			wantCLen:    "2",
		},
		{
			name:        "start at or beyond size 416",
			rangeHeader: "bytes=10-20",
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			wantBody:    "",
			wantCRange:  "bytes */10",
		},
		{
			name:        "absurd overflow start 416",
			rangeHeader: "bytes=99999999999999999999-",
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			wantBody:    "",
			wantCRange:  "bytes */10",
		},
		{
			name:        "absurd overflow end ignored full body",
			rangeHeader: "bytes=0-99999999999999999999",
			wantStatus:  http.StatusOK,
			wantBody:    "0123456789",
			wantCRange:  "",
			wantCLen:    "10",
		},
		{
			name:        "negative start ignored full body",
			rangeHeader: "bytes=-",
			wantStatus:  http.StatusOK,
			wantBody:    "0123456789",
			wantCRange:  "",
			wantCLen:    "10",
		},
		{
			name:        "malformed range ignored full body",
			rangeHeader: "bytes=abc-def",
			wantStatus:  http.StatusOK,
			wantBody:    "0123456789",
			wantCRange:  "",
			wantCLen:    "10",
		},
		{
			name:        "unknown unit ignored full body",
			rangeHeader: "items=0-3",
			wantStatus:  http.StatusOK,
			wantBody:    "0123456789",
			wantCRange:  "",
			wantCLen:    "10",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := getObjectWithRange(t, bucket, key, tc.rangeHeader)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d; want %d (body=%q)", w.Code, tc.wantStatus, w.Body.String())
			}
			// A 416 carries the empty error-shaped body; the 200/206 cases must
			// return exactly the expected byte slice.
			if tc.wantStatus != http.StatusRequestedRangeNotSatisfiable {
				if got := w.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %q; want %q", got, tc.wantBody)
				}
			}
			assertHeader(t, w, "Content-Range", tc.wantCRange)
			if tc.wantCLen != "" {
				assertHeader(t, w, "Content-Length", tc.wantCLen)
			}
			// Every response advertises range capability and a stable identity.
			assertHeader(t, w, "Accept-Ranges", "bytes")
			assertHeader(t, w, "ETag", wantETag)
		})
	}
}

// assertHeader fails the test if the recorded response header does not match
// the expected value exactly (an empty want asserts the header is absent).
func assertHeader(t *testing.T, w *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := w.Header().Get(name); got != want {
		t.Fatalf("%s = %q; want %q", name, got, want)
	}
}

// The sidecar Content-Type must survive the ServeContent path on both full and
// partial reads — ServeContent must not sniff and overwrite it. A drift here
// reopens the stored-XSS sniffing class the nosniff header guards against.
func TestDownloadObjectRangePreservesContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	bucket := "ctbkt"
	key := "doc.bin"
	// HTML bytes deliberately stored under a non-HTML type: if ServeContent
	// sniffed, it would label this text/html and defeat the guard.
	seedObject(t, bucket, key, []byte("<html>not html really</html>"))

	for _, rng := range []string{"", "bytes=0-3"} {
		w := getObjectWithRange(t, bucket, key, rng)
		if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("range=%q Content-Type = %q; want application/octet-stream", rng, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("range=%q X-Content-Type-Options = %q; want nosniff", rng, got)
		}
	}
}

// HEAD must advertise Accept-Ranges so a client probing before a ranged GET
// sees the same capability the GET path emits.
func TestHeadObjectAdvertisesAcceptRanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	bucket := "headbkt"
	key := "obj.bin"
	seedObject(t, bucket, key, []byte("payload"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}, {Key: "objectKey", Value: "/" + key}}
	c.Request = httptest.NewRequest(http.MethodHead, "/"+bucket+"/"+key, nil)
	GetObjectMetadataHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("HEAD Accept-Ranges = %q; want %q", got, "bytes")
	}
}
