package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// setupStorage initializes BoltDB under a temp dir and creates a test user.
// Returns accessKey and secret. Caller must run in gin.TestMode.
func setupStorage(t *testing.T) (string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	storage.SetEncryptionKey(key)

	// Unique DB file per test run to avoid cross-test contamination.
	dbName := fmt.Sprintf("users-%d.db", time.Now().UnixNano())
	if err := storage.InitUserStore(dbName); err != nil {
		t.Fatalf("InitUserStore: %v", err)
	}

	secret := "testsecret1234567890"
	encrypted, err := storage.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ak := fmt.Sprintf("AK%d", time.Now().UnixNano())
	u := &storage.User{
		AccessKeyID:     ak,
		EncryptedSecret: encrypted,
		ACL: []storage.ACLRule{
			{Effect: "Allow", Buckets: []string{"*"}, Actions: []string{"*"}},
		},
	}
	if err := storage.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return ak, secret
}

// signRequest signs an *http.Request with SigV4 header-auth style.
// body is the exact payload; payloadHashOverride, when non-empty, replaces the computed hash
// in the X-Amz-Content-Sha256 header and canonical request (to simulate mismatch attacks).
func signRequest(t *testing.T, req *http.Request, body []byte, accessKey, secret, region, service string, now time.Time, payloadHashOverride string) {
	t.Helper()
	amzDate := now.UTC().Format("20060102T150405Z")
	dateOnly := amzDate[:8]

	payloadHash := hashSHA256(string(body))
	headerPayload := payloadHash
	if payloadHashOverride != "" {
		headerPayload = payloadHashOverride
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", headerPayload)
	if req.Host == "" {
		req.Host = "example.com"
	}

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	// canonical headers
	ch := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		req.Host, headerPayload, amzDate)

	canonicalQuery := ""
	if req.URL.RawQuery != "" {
		canonicalQuery = req.URL.RawQuery
	}
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery,
		ch,
		signedHeaders,
		headerPayload,
	)

	credScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateOnly, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credScope, hashSHA256(canonicalRequest))
	signingKey := getSigningKey("AWS4"+secret, dateOnly, region, service)
	sig := hex.EncodeToString(hmacSHA256([]byte(stringToSign), signingKey))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		accessKey, credScope, signedHeaders, sig)
	req.Header.Set("Authorization", auth)
}

// buildEngine returns a minimal router that exposes /test behind AuthMiddleware.
// The handler signals by writing a canary body so tests can assert downstream execution.
func buildEngine(handlerCalled *bool) *gin.Engine {
	r := gin.New()
	r.Use(AuthMiddleware)
	fn := func(c *gin.Context) {
		if handlerCalled != nil {
			*handlerCalled = true
		}
		c.String(http.StatusOK, "ok")
	}
	r.GET("/test", fn)
	r.PUT("/test", fn)
	r.PUT("/:bucket/*objectKey", fn)
	return r
}

// Bug 1: behavioural — a tampered signature must be rejected with 401 SignatureDoesNotMatch.
func TestAuth_Bug1_TamperedSignatureRejected(t *testing.T) {
	ak, secret := setupStorage(t)

	body := []byte("hello")
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", time.Now(), "")

	// Tamper the signature: flip the last char.
	auth := req.Header.Get("Authorization")
	tampered := auth[:len(auth)-1]
	if auth[len(auth)-1] == 'a' {
		tampered += "b"
	} else {
		tampered += "a"
	}
	req.Header.Set("Authorization", tampered)

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SignatureDoesNotMatch") {
		t.Fatalf("expected SignatureDoesNotMatch in body, got %s", w.Body.String())
	}
	if called {
		t.Fatal("handler must not be invoked on tampered signature")
	}
}

// Bug 1: positive — a correctly-signed request passes.
func TestAuth_Bug1_ValidSignaturePasses(t *testing.T) {
	ak, secret := setupStorage(t)

	body := []byte("hello")
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", time.Now(), "")

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("handler must be invoked on valid signature")
	}
}

// keep unused imports pulled in
var _ = io.Discard
