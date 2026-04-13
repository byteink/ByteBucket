package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
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

// Bug 2: server must verify body SHA-256 against X-Amz-Content-Sha256.
// When the signed header claims a payload hash that does not match the body,
// the request is rejected with 400 XAmzContentSHA256Mismatch.
func TestAuth_Bug2_PayloadHashMismatchRejected(t *testing.T) {
	ak, secret := setupStorage(t)

	body := []byte("actual body bytes")
	// Sign a request whose X-Amz-Content-Sha256 claims a *different* payload.
	// The claimed hash must be used consistently in the signature so the
	// signature itself verifies; then the server must still catch that the
	// body does not match the claimed hash.
	fakeHash := hashSHA256("a totally different body")
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", time.Now(), fakeHash)

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "XAmzContentSHA256Mismatch") &&
		!strings.Contains(w.Body.String(), "BadDigest") {
		t.Fatalf("expected XAmzContentSHA256Mismatch/BadDigest, got %s", w.Body.String())
	}
	if called {
		t.Fatal("handler must not run when payload hash mismatches body")
	}
}

// Bug 2: UNSIGNED-PAYLOAD and STREAMING-* payload hashes skip verification.
func TestAuth_Bug2_UnsignedPayloadSkipsVerification(t *testing.T) {
	ak, secret := setupStorage(t)

	body := []byte("anything")
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", time.Now(), "UNSIGNED-PAYLOAD")

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for UNSIGNED-PAYLOAD, got %d, body=%s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("handler must run when payload hash is UNSIGNED-PAYLOAD")
	}
}

// Bug 2: body must remain readable by downstream handlers after verification.
func TestAuth_Bug2_BodyPreservedForHandler(t *testing.T) {
	ak, secret := setupStorage(t)

	body := []byte("preserved payload")
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", time.Now(), "")

	// Custom engine that echoes the body.
	r := gin.New()
	r.Use(AuthMiddleware)
	r.PUT("/test", func(c *gin.Context) {
		b, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.String(http.StatusInternalServerError, "read: %v", err)
			return
		}
		c.Data(http.StatusOK, "application/octet-stream", b)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("body not preserved: got %q want %q", w.Body.Bytes(), body)
	}
}

// Bug 3: when validateTimestamp rejects, the middleware must return
// immediately. In the buggy version execution continued into isUserAllowed,
// which can overwrite the 401 response with a 403 (or worse, double-write).
// A user with a restrictive ACL surfaces this: on a stale timestamp the
// response must be 401, not 403.
func TestAuth_Bug3_StaleTimestampStopsBeforeACL(t *testing.T) {
	// Bootstrap storage and create a user with an ACL that denies the
	// target action, so the buggy post-validateTimestamp path would
	// produce a 403.
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
	if err := storage.InitUserStore(fmt.Sprintf("users-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitUserStore: %v", err)
	}
	secret := "restrictedsecret"
	enc, err := storage.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ak := fmt.Sprintf("RK%d", time.Now().UnixNano())
	if err := storage.CreateUser(&storage.User{
		AccessKeyID:     ak,
		EncryptedSecret: enc,
		ACL: []storage.ACLRule{
			{Effect: "Allow", Buckets: []string{"only-this"}, Actions: []string{"s3:GetObject"}},
		},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	body := []byte("x")
	stale := time.Now().Add(-30 * time.Minute)
	req := httptest.NewRequest(http.MethodPut, "/test", bytes.NewReader(body))
	signRequest(t, req, body, ak, secret, "us-east-1", "s3", stale, "")

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from timestamp rejection, got %d, body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("handler must not run for stale timestamp")
	}
	// Body must contain exactly one XML Error document. In the buggy
	// version the middleware kept executing after validateTimestamp and
	// the subsequent ACL denial produced a second XML body.
	if n := strings.Count(w.Body.String(), "<Error>"); n != 1 {
		t.Fatalf("expected exactly one <Error> element, got %d, body=%s", n, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Request timestamp expired") {
		t.Fatalf("expected timestamp-expired message, got %s", w.Body.String())
	}
}

// signPresignedURL constructs a SigV4 presigned URL. If includeHashInQuery
// is false, X-Amz-Content-Sha256 is NOT added to the query parameters, but
// the client nonetheless signs as if payloadHash were the payload hash.
// This mirrors the real-world scenario in Bug 4.
func signPresignedURL(t *testing.T, method, target, accessKey, secret, region, service, payloadHash string, includeHashInQuery bool) string {
	t.Helper()
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := amzDate[:8]

	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	credScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", accessKey+"/"+credScope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", "900")
	q.Set("X-Amz-SignedHeaders", "host")
	if includeHashInQuery {
		q.Set("X-Amz-Content-Sha256", payloadHash)
	}
	// Do not set X-Amz-Signature yet.
	u.RawQuery = q.Encode()

	// Build canonical query manually (same ordering as buildCanonicalQuery).
	var parts []string
	for k, vs := range u.Query() {
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	sort.Strings(parts)
	canonicalQuery := strings.Join(parts, "&")

	canonicalHeaders := fmt.Sprintf("host:%s\n", u.Host)
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method, u.EscapedPath(), canonicalQuery, canonicalHeaders, "host", payloadHash)

	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credScope, hashSHA256(canonicalRequest))
	sig := hex.EncodeToString(hmacSHA256([]byte(stringToSign), getSigningKey("AWS4"+secret, date, region, service)))

	q.Set("X-Amz-Signature", sig)
	u.RawQuery = q.Encode()
	return u.String()
}

// Bug 4: presigned URL where client signed with "UNSIGNED-PAYLOAD" but did
// not include X-Amz-Content-Sha256 in the query. The old server silently
// defaulted to "UNSIGNED-PAYLOAD", producing a match. The fix uses the
// literal (empty) query value and the signature must therefore fail.
func TestAuth_Bug4_PresignedDoesNotDefaultPayloadHash(t *testing.T) {
	ak, secret := setupStorage(t)

	presigned := signPresignedURL(t, http.MethodGet,
		"http://example.com/test", ak, secret, "us-east-1", "s3",
		"UNSIGNED-PAYLOAD", false /* do not include in query */)

	req := httptest.NewRequest(http.MethodGet, presigned, nil)
	req.Host = "example.com"

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 SignatureDoesNotMatch, got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SignatureDoesNotMatch") {
		t.Fatalf("expected SignatureDoesNotMatch, got %s", w.Body.String())
	}
	if called {
		t.Fatal("handler must not run")
	}
}

// Positive: presigned URL that does include X-Amz-Content-Sha256 in query
// and signed with that same value must pass.
func TestAuth_Bug4_PresignedWithHashInQueryPasses(t *testing.T) {
	ak, secret := setupStorage(t)

	presigned := signPresignedURL(t, http.MethodGet,
		"http://example.com/test", ak, secret, "us-east-1", "s3",
		"UNSIGNED-PAYLOAD", true)

	req := httptest.NewRequest(http.MethodGet, presigned, nil)
	req.Host = "example.com"

	called := false
	w := httptest.NewRecorder()
	buildEngine(&called).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("handler must run when presigned URL is valid")
	}
}

// keep unused imports pulled in
var _ = io.Discard
