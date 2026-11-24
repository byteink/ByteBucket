package auth

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

type trackedPayload struct {
	reader io.Reader
	reads  int
	max    int
}

func (b *trackedPayload) Read(p []byte) (int, error) {
	b.reads++
	if len(p) > b.max {
		b.max = len(p)
	}
	return b.reader.Read(p)
}

func (b *trackedPayload) Close() error { return nil }

func TestHeaderAuthRejectsBeforeReadingPayload(t *testing.T) {
	for _, name := range []string{"signature", "unknown user", "forbidden", "expired"} {
		t.Run(name, func(t *testing.T) {
			ak, secret := setupStorage(t)
			body := &trackedPayload{reader: strings.NewReader("payload")}
			req := httptest.NewRequest(http.MethodPut, "/allowed/file", body)
			now := time.Now()
			want := http.StatusUnauthorized
			if name == "unknown user" {
				ak = "missing-user"
			}
			if name == "expired" {
				now = now.Add(-time.Hour)
			}
			if name == "forbidden" {
				want = http.StatusForbidden
				if err := storage.UpdateUserACL(ak, nil); err != nil {
					t.Fatal(err)
				}
			}
			signRequest(t, req, []byte("payload"), ak, secret, "us-east-1", "s3", now, "")
			if name == "signature" {
				req.Header.Set("Authorization", req.Header.Get("Authorization")+"0")
			}
			response := httptest.NewRecorder()
			called := false
			buildEngine(&called).ServeHTTP(response, req)
			if response.Code != want || called {
				t.Fatalf("status=%d, handler=%t; want %d without handler", response.Code, called, want)
			}
			if body.reads != 0 {
				t.Fatalf("rejected request read payload %d times", body.reads)
			}
		})
	}
}

func TestHeaderAuthPayloadUsesBoundedReads(t *testing.T) {
	ak, secret := setupStorage(t)
	root := t.TempDir()
	oldRoot := storage.UploadsRoot
	storage.UploadsRoot = root
	t.Cleanup(func() { storage.UploadsRoot = oldRoot })
	payload := bytes.Repeat([]byte("large signed payload"), 1<<16)
	body := &trackedPayload{reader: bytes.NewReader(payload)}
	req := httptest.NewRequest(http.MethodPut, "/test", body)
	signRequest(t, req, payload, ak, secret, "us-east-1", "s3", time.Now(), "")
	r := gin.New()
	r.Use(AuthMiddleware)
	r.PUT("/test", func(c *gin.Context) {
		digest := sha256Payload(t, c.Request.Body)
		if digest != hashSHA256(string(payload)) {
			t.Fatal("verified payload changed before handler")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("payload staging files remain visible: %v, %v", entries, err)
		}
		c.Status(http.StatusOK)
	})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", response.Code, response.Body.String())
	}
	if body.max > 32<<10 {
		t.Fatalf("payload read buffer grew to %d bytes; limit 32768", body.max)
	}
	if req.Body != body {
		t.Fatal("original request body was not restored")
	}
}

func sha256Payload(t *testing.T, reader io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return hashSHA256(string(data))
}

type failingPayload struct{}

func (failingPayload) Read([]byte) (int, error) { return 0, errors.New("payload read failed") }

func TestHeaderAuthPayloadReadFailure(t *testing.T) {
	ak, secret := setupStorage(t)
	req := httptest.NewRequest(http.MethodPut, "/test", failingPayload{})
	signRequest(t, req, nil, ak, secret, "us-east-1", "s3", time.Now(), "")
	response := httptest.NewRecorder()
	called := false
	buildEngine(&called).ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "IncompleteBody") || called {
		t.Fatalf("status=%d, body=%s, handler=%t", response.Code, response.Body.String(), called)
	}
}

func TestHeaderAuthSpoolUnavailable(t *testing.T) {
	ak, secret := setupStorage(t)
	path := t.TempDir() + "/not-directory"
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	storage.UploadsRoot = path
	body := &trackedPayload{reader: strings.NewReader("payload")}
	req := httptest.NewRequest(http.MethodPut, "/test", body)
	signRequest(t, req, []byte("payload"), ak, secret, "us-east-1", "s3", time.Now(), "")
	response := httptest.NewRecorder()
	called := false
	buildEngine(&called).ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError || called || body.reads != 0 {
		t.Fatalf("status=%d, handler=%t, reads=%d", response.Code, called, body.reads)
	}
}

func TestOpenPayloadSpoolCreateFailure(t *testing.T) {
	previous := storage.UploadsRoot
	// The descriptor filesystem permits lookup but cannot create staging files.
	storage.UploadsRoot = "/dev/fd"
	t.Cleanup(func() { storage.UploadsRoot = previous })
	if file, err := openPayloadSpool(); err == nil {
		closePayloadSpool(file)
		t.Fatal("expected temporary file creation to fail")
	}
}

func TestUnlinkPayloadSpoolFailureClosesFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "payload")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := unlinkPayloadSpool(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected already-unlinked error, got %v", err)
	}
	if _, err := file.WriteString("payload"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("unlinked error left file open: %v", err)
	}
	closePayloadSpool(file)
}

type closeAtEOFPayload struct{ file *os.File }

func (r closeAtEOFPayload) Read([]byte) (int, error) {
	if err := r.file.Close(); err != nil {
		return 0, err
	}
	return 0, io.EOF
}

func TestVerifyPayloadSpoolFileFailures(t *testing.T) {
	for _, name := range []string{"write", "seek"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "payload")
			if err != nil {
				t.Fatal(err)
			}
			var source io.Reader = closeAtEOFPayload{file: file}
			if name == "write" {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				source = strings.NewReader("payload")
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPut, "/test", source)
			if verifyPayloadSpool(c, file, hashSHA256("")) || response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d, body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHeaderAuthStreamingPayloadUnchanged(t *testing.T) {
	ak, secret := setupStorage(t)
	body := &trackedPayload{reader: strings.NewReader("streamed payload")}
	req := httptest.NewRequest(http.MethodPut, "/test", body)
	signRequest(t, req, nil, ak, secret, "us-east-1", "s3", time.Now(), "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
	response := httptest.NewRecorder()
	called := false
	buildEngine(&called).ServeHTTP(response, req)
	if response.Code != http.StatusOK || !called || body.reads != 0 || req.Body != body {
		t.Fatalf("status=%d, handler=%t, reads=%d", response.Code, called, body.reads)
	}
}

func TestHeaderAuthMalformedPayloadHash(t *testing.T) {
	ak, secret := setupStorage(t)
	req := httptest.NewRequest(http.MethodPut, "/test", strings.NewReader("payload"))
	signRequest(t, req, nil, ak, secret, "us-east-1", "s3", time.Now(), "malformed")
	response := httptest.NewRecorder()
	called := false
	buildEngine(&called).ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || called || !strings.Contains(response.Body.String(), "XAmzContentSHA256Mismatch") {
		t.Fatalf("status=%d, body=%s, handler=%t", response.Code, response.Body.String(), called)
	}
}

func TestHeaderAuthMalformedHeadersDoNotReadPayload(t *testing.T) {
	credential := "Credential=key/20000101/us-east-1/s3/aws4_request"
	for _, tc := range []struct {
		name string
		auth string
	}{
		{"missing credential", "AWS4-HMAC-SHA256 invalid"},
		{"missing signed headers", "AWS4-HMAC-SHA256 " + credential},
		{"missing signature", "AWS4-HMAC-SHA256 " + credential + ",SignedHeaders=host"},
		{"invalid credential", "AWS4-HMAC-SHA256 Credential=key,SignedHeaders=host,Signature=00"},
		{"invalid terminal", "AWS4-HMAC-SHA256 Credential=key/20000101/us-east-1/s3/wrong,SignedHeaders=host,Signature=00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedPayload{reader: strings.NewReader("payload")}
			req := httptest.NewRequest(http.MethodPut, "/test", body)
			req.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			buildEngine(nil).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || body.reads != 0 {
				t.Fatalf("status=%d, reads=%d", response.Code, body.reads)
			}
		})
	}
}

func TestHeaderAuthMissingDateOrHash(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		value  string
	}{
		{"missing date", "X-Amz-Date", ""},
		{"short date", "X-Amz-Date", "today"},
		{"mismatched date", "X-Amz-Date", "20000101T000000Z"},
		{"missing hash", "X-Amz-Content-Sha256", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ak, secret := setupStorage(t)
			body := &trackedPayload{reader: strings.NewReader("payload")}
			req := httptest.NewRequest(http.MethodPut, "/test", body)
			signRequest(t, req, []byte("payload"), ak, secret, "us-east-1", "s3", time.Now(), "")
			req.Header.Set(tc.header, tc.value)
			response := httptest.NewRecorder()
			buildEngine(nil).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || body.reads != 0 {
				t.Fatalf("status=%d, reads=%d", response.Code, body.reads)
			}
		})
	}
}

func TestHeaderAuthSecretDecryptFailureDoesNotReadPayload(t *testing.T) {
	setupStorage(t)
	if err := storage.CreateUser(&storage.User{AccessKeyID: "broken-secret", EncryptedSecret: "invalid"}); err != nil {
		t.Fatal(err)
	}
	body := &trackedPayload{reader: strings.NewReader("payload")}
	req := httptest.NewRequest(http.MethodPut, "/test", body)
	signRequest(t, req, []byte("payload"), "broken-secret", "secret", "us-east-1", "s3", time.Now(), "")
	response := httptest.NewRecorder()
	buildEngine(nil).ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError || body.reads != 0 {
		t.Fatalf("status=%d, reads=%d", response.Code, body.reads)
	}
}

func TestPresignedAuthRejectsForbiddenBucket(t *testing.T) {
	ak, secret := setupStorage(t)
	if err := storage.UpdateUserACL(ak, []storage.ACLRule{
		{Effect: "Allow", Buckets: []string{"allowed"}, Actions: []string{"s3:GetObject"}},
	}); err != nil {
		t.Fatal(err)
	}
	url := signPresignedURL(t, http.MethodGet, "http://example.com/forbidden/file", ak, secret,
		"us-east-1", "s3", "UNSIGNED-PAYLOAD", "host", false)
	r := gin.New()
	r.Use(AuthMiddleware)
	called := false
	r.GET("/:bucket/*objectKey", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("status=%d, handler=%t", response.Code, called)
	}
}
