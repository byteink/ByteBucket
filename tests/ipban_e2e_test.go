package tests

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const ipBanPath = "/api/config/ipban"

func setIPBan(t *testing.T, body string) {
	t.Helper()
	if status, b := adminJSON(t, http.MethodPut, ipBanPath, body); status != http.StatusOK {
		t.Fatalf("set ip ban %s: %d %s", body, status, b)
	}
}

// enableIPBanBehindXFF trusts X-Forwarded-For so the test can speak as distinct
// public clients, and enables the ban. Cleanup clears the override (which also
// flushes every ban) and the trusted header, so the shared container is left
// as it was found.
func enableIPBanBehindXFF(t *testing.T, body string) {
	t.Helper()
	setTrustedProxy(t, `{"headers":["X-Forwarded-For"],"useLeftmostIP":false}`)
	setIPBan(t, body)
	t.Cleanup(func() {
		_, _ = adminJSON(t, http.MethodDelete, ipBanPath, "")
		setTrustedProxy(t, `{"headers":[],"useLeftmostIP":false}`)
	})
}

// anonGet sends an unsigned GET to the storage port with an optional
// X-Forwarded-For, returning the status and body.
func anonGet(t *testing.T, path, xff string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, storageURL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp.StatusCode, readAllClose(t, resp)
}

func expectAnon(t *testing.T, path, xff string, want int) []byte {
	t.Helper()
	got, body := anonGet(t, path, xff)
	if got != want {
		t.Fatalf("GET %s xff=%q = %d, want %d body=%s", path, xff, got, want, body)
	}
	return body
}

// TestE2E_IPBanBlocksScanner reproduces the production scanner pattern
// (anonymous guesses at bucket/key names, all 401) and proves the ban engages
// at the threshold, refuses even an otherwise-public read with S3
// AccessDenied, stays scoped to the offending IP, and lifts on schedule even
// while the banned client keeps knocking.
func TestE2E_IPBanBlocksScanner(t *testing.T) {
	enableIPBanBehindXFF(t, `{"enabled":true,"maxFailures":3,"windowSeconds":60,"banSeconds":2}`)

	const bucket = "ipban-e2e"
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	if _, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("pub.txt"), Body: strings.NewReader("public"),
	}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if status, b := adminJSON(t, http.MethodPut, "/api/s3/"+bucket+"?acl", `{"canned":"public-read"}`); status != http.StatusOK {
		t.Fatalf("make bucket public: %d %s", status, b)
	}

	const scanner = "203.0.113.50"
	for _, p := range []string{"/backups/woocommerce.sql", "/logs/give_stripe_settings.zip", "/db/dump.sql"} {
		expectAnon(t, p, scanner, http.StatusUnauthorized)
	}
	body := expectAnon(t, "/"+bucket+"/pub.txt", scanner, http.StatusForbidden)
	var e struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &e); err != nil || e.Code != "AccessDenied" {
		t.Fatalf("ban body = %s (%v), want S3 AccessDenied", body, err)
	}
	expectAnon(t, "/"+bucket+"/pub.txt", "203.0.113.51", http.StatusOK)

	// Keep knocking while banned: a ratchet would push the expiry out past 2s.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		expectAnon(t, "/backups/again.sql", scanner, http.StatusForbidden)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(700 * time.Millisecond)
	expectAnon(t, "/"+bucket+"/pub.txt", scanner, http.StatusOK)
}

// TestE2E_IPBanIgnores404 proves a client that only produces 404s (imgproxy
// fetching deleted objects) is never banned.
func TestE2E_IPBanIgnores404(t *testing.T) {
	enableIPBanBehindXFF(t, `{"enabled":true,"maxFailures":2,"windowSeconds":60,"banSeconds":900}`)

	const bucket = "ipban-404-e2e"
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	if _, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	const fetcher = "203.0.113.60"
	for i := 0; i < 5; i++ {
		resp := sigV4Do(t, http.MethodGet, "/"+bucket+"/deleted.jpg", nil, map[string]string{"X-Forwarded-For": fetcher})
		_ = readAllClose(t, resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("signed GET of missing key = %d, want 404", resp.StatusCode)
		}
	}
	expectAnon(t, "/"+bucket+"/deleted.jpg", fetcher, http.StatusUnauthorized)
}

// TestE2E_IPBanNeverBansProxyAddress covers the misconfiguration that would
// otherwise take the service down: when the trusted header is absent every
// request resolves to the socket peer (here the Docker gateway, a private
// address), and a forged private X-Forwarded-For is equally exempt.
func TestE2E_IPBanNeverBansProxyAddress(t *testing.T) {
	enableIPBanBehindXFF(t, `{"enabled":true,"maxFailures":2,"windowSeconds":60,"banSeconds":900}`)
	for i := 0; i < 6; i++ {
		expectAnon(t, "/backups/woocommerce.sql", "", http.StatusUnauthorized)
		expectAnon(t, "/backups/woocommerce.sql", "10.20.30.40", http.StatusUnauthorized)
		expectAnon(t, "/backups/woocommerce.sql", "100.64.1.2", http.StatusUnauthorized)
	}
}

// TestE2E_IPBanConfig covers the admin round-trip and boundary validation.
func TestE2E_IPBanConfig(t *testing.T) {
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodDelete, ipBanPath, "") })

	// Admin-only. Bad credentials are not tried here: each one counts toward
	// the admin lockout shared with TestE2E_AdminManagementAuthBoundary.
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(m, adminURL+ipBanPath, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("anon %s: %v", m, err)
		}
		_ = readAllClose(t, resp)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anon %s %s = %d, want 401", m, ipBanPath, resp.StatusCode)
		}
	}

	status, body := adminJSON(t, http.MethodGet, ipBanPath, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"effective":{"enabled":false,"maxFailures":20,"windowSeconds":60,"banSeconds":900}`) {
		t.Fatalf("default GET = %d %s", status, body)
	}
	for _, bad := range []string{
		`{"enabled":true,"maxFailures":0,"windowSeconds":60,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":3601,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":604801}`,
		`not-json`,
	} {
		if status, b := adminJSON(t, http.MethodPut, ipBanPath, bad); status != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d %s, want 400", bad, status, b)
		}
	}
	setIPBan(t, `{"enabled":false,"maxFailures":7,"windowSeconds":30,"banSeconds":120}`)
	_, body = adminJSON(t, http.MethodGet, ipBanPath, "")
	if !strings.Contains(string(body), `"override":{"enabled":false,"maxFailures":7,"windowSeconds":30,"banSeconds":120}`) {
		t.Fatalf("override not reflected: %s", body)
	}
	if status, _ := adminJSON(t, http.MethodDelete, ipBanPath, ""); status != http.StatusOK {
		t.Fatalf("DELETE: %d", status)
	}
	if _, body = adminJSON(t, http.MethodGet, ipBanPath, ""); !strings.Contains(string(body), `"override":null`) {
		t.Fatalf("override still present: %s", body)
	}
}
