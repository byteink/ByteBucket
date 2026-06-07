package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// adminJSON sends an authenticated admin-API request and returns the status and
// raw body. Centralises the boilerplate the management-API tests share.
func adminJSON(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	resp := adminDo(t, adminRequest(t, method, path, b, "application/json"))
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// createManagedUser creates a user with the given ACL JSON via POST /api/users
// and returns its credentials. Fails the test on any non-201.
func createManagedUser(t *testing.T, aclJSON string) (accessKey, secret string) {
	t.Helper()
	status, body := adminJSON(t, http.MethodPost, "/api/users", `{"acl":`+aclJSON+`}`)
	if status != http.StatusCreated {
		t.Fatalf("create user: status %d body %s", status, body)
	}
	var u struct {
		AccessKeyID     string `json:"accessKeyID"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if u.AccessKeyID == "" || u.SecretAccessKey == "" {
		t.Fatalf("create returned empty credentials: %s", body)
	}
	return u.AccessKeyID, u.SecretAccessKey
}

// TestE2E_AdminConfig covers GET /api/config: it returns the public base URL and
// must never leak server secrets into the config surface.
func TestE2E_AdminConfig(t *testing.T) {
	status, body := adminJSON(t, http.MethodGet, "/api/config", "")
	if status != http.StatusOK {
		t.Fatalf("GET config: %d", status)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode config: %v body=%s", err, body)
	}
	if _, ok := cfg["publicBaseURL"]; !ok {
		t.Fatalf("config missing publicBaseURL: %s", body)
	}
	// The config surface must not expose any secret material.
	for _, bad := range []string{"secretAccessKey", "encryptedSecret", "ENCRYPTION", "encryptionKey"} {
		if strings.Contains(string(body), bad) {
			t.Fatalf("config leaked %q: %s", bad, body)
		}
	}
}

// TestE2E_AdminUserLifecycle drives the full management-API user lifecycle:
// create, list (no secret leak), update ACL, and delete — plus the 404 contract
// for updating a user that does not exist.
func TestE2E_AdminUserLifecycle(t *testing.T) {
	ak, secret := createManagedUser(t, `[{"effect":"Allow","buckets":["lifecycle-a"],"actions":["*"]}]`)
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodDelete, "/api/users/"+ak, "") })

	// List must include the new user with its ACL, and must NEVER return the
	// secret (plaintext or encrypted).
	status, body := adminJSON(t, http.MethodGet, "/api/users", "")
	if status != http.StatusOK {
		t.Fatalf("list users: %d", status)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("user list leaked the plaintext secret")
	}
	if strings.Contains(string(body), "encryptedSecret") || strings.Contains(string(body), "EncryptedSecret") {
		t.Fatalf("user list leaked the encrypted secret field")
	}
	if buckets := aclBucketsFor(t, body, ak); !contains(buckets, "lifecycle-a") {
		t.Fatalf("created user ACL not in list, got buckets %v", buckets)
	}

	// Update the ACL, then confirm the list reflects the new bucket scope.
	status, _ = adminJSON(t, http.MethodPut, "/api/users/"+ak,
		`{"acl":[{"effect":"Allow","buckets":["lifecycle-b"],"actions":["*"]}]}`)
	if status != http.StatusOK {
		t.Fatalf("update user ACL: %d", status)
	}
	_, body = adminJSON(t, http.MethodGet, "/api/users", "")
	if buckets := aclBucketsFor(t, body, ak); !contains(buckets, "lifecycle-b") || contains(buckets, "lifecycle-a") {
		t.Fatalf("ACL update not reflected in list, got buckets %v", buckets)
	}

	// Updating a user that does not exist is a 404, not a 500.
	if status, _ := adminJSON(t, http.MethodPut, "/api/users/does-not-exist",
		`{"acl":[{"effect":"Allow","buckets":["x"],"actions":["*"]}]}`); status != http.StatusNotFound {
		t.Fatalf("update nonexistent user = %d, want 404", status)
	}

	// Delete, then confirm the user is gone from the list.
	if status, _ := adminJSON(t, http.MethodDelete, "/api/users/"+ak, ""); status != http.StatusNoContent {
		t.Fatalf("delete user = %d, want 204", status)
	}
	_, body = adminJSON(t, http.MethodGet, "/api/users", "")
	if strings.Contains(string(body), ak) {
		t.Fatalf("deleted user still present in list")
	}
}

// TestE2E_AdminUserACLUpdateEnforced proves PUT /api/users/:id is wired to
// enforcement: a user denied a bucket gains access once their ACL is updated.
func TestE2E_AdminUserACLUpdateEnforced(t *testing.T) {
	const allowed = "aclupd-allowed"
	const denied = "aclupd-denied"
	ak, sk := createManagedUser(t, `[{"effect":"Allow","buckets":["`+allowed+`"],"actions":["*"]}]`)
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodDelete, "/api/users/"+ak, "") })

	client := createS3Client(ak, sk)

	// Before the update: creating the un-scoped bucket must be denied.
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(denied)})
	if err == nil {
		t.Fatal("restricted user created an unauthorized bucket before ACL update")
	}

	// Grant access to the previously-denied bucket.
	if status, _ := adminJSON(t, http.MethodPut, "/api/users/"+ak,
		`{"acl":[{"effect":"Allow","buckets":["`+allowed+`","`+denied+`"],"actions":["*"]}]}`); status != http.StatusOK {
		t.Fatalf("update ACL: %d", status)
	}

	// After the update: the same operation now succeeds.
	if _, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(denied)}); err != nil {
		t.Fatalf("user still denied after ACL grant: %v", err)
	}
	t.Cleanup(func() {
		admin := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
		_, _ = admin.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(denied)})
	})
}

// TestE2E_AdminRateLimitConfig covers the GET/PUT/DELETE round-trip of the
// runtime rate-limit override. The override is set with limiting DISABLED so it
// cannot throttle the rest of the shared-container suite, and is always cleared.
func TestE2E_AdminRateLimitConfig(t *testing.T) {
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodDelete, "/api/config/ratelimit", "") })

	status, body := adminJSON(t, http.MethodGet, "/api/config/ratelimit", "")
	if status != http.StatusOK {
		t.Fatalf("GET ratelimit: %d", status)
	}
	var st struct {
		Override *struct {
			RPS float64 `json:"rps"`
		} `json:"override"`
		Effective struct {
			RPS float64 `json:"rps"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode ratelimit state: %v body=%s", err, body)
	}

	// Persist an override (disabled, distinctive rps) and confirm it becomes effective.
	status, _ = adminJSON(t, http.MethodPut, "/api/config/ratelimit",
		`{"enabled":false,"rps":7,"burst":9,"trustedProxies":1}`)
	if status != http.StatusOK {
		t.Fatalf("PUT ratelimit: %d", status)
	}
	_, body = adminJSON(t, http.MethodGet, "/api/config/ratelimit", "")
	_ = json.Unmarshal(body, &st)
	if st.Override == nil || st.Override.RPS != 7 || st.Effective.RPS != 7 {
		t.Fatalf("override not applied: %s", body)
	}

	// Clearing the override reverts to the environment baseline.
	if status, _ := adminJSON(t, http.MethodDelete, "/api/config/ratelimit", ""); status != http.StatusOK {
		t.Fatalf("DELETE ratelimit: %d", status)
	}
	_, body = adminJSON(t, http.MethodGet, "/api/config/ratelimit", "")
	_ = json.Unmarshal(body, &st)
	if st.Override != nil {
		t.Fatalf("override still present after delete: %s", body)
	}
}

// TestE2E_AdminManagementAuthBoundary asserts every management-API endpoint
// rejects unauthenticated and bad-credential requests. This is the deny-by-
// default gate the whole admin surface depends on.
func TestE2E_AdminManagementAuthBoundary(t *testing.T) {
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/users"},
		{http.MethodPost, "/api/users"},
		{http.MethodPut, "/api/users/x"},
		{http.MethodDelete, "/api/users/x"},
		{http.MethodGet, "/api/config/ratelimit"},
		{http.MethodPut, "/api/config/ratelimit"},
		{http.MethodDelete, "/api/config/ratelimit"},
	}
	for _, e := range endpoints {
		// No credentials: 401.
		req, _ := http.NewRequest(e.method, adminURL+e.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("anon %s %s: %v", e.method, e.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anon %s %s = %d, want 401", e.method, e.path, resp.StatusCode)
		}

		// Wrong credentials: still denied (401 or 403, never 2xx).
		req2, _ := http.NewRequest(e.method, adminURL+e.path, bytes.NewReader([]byte("{}")))
		req2.Header.Set("X-Admin-AccessKey", "wrong")
		req2.Header.Set("X-Admin-Secret", "wrong")
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("badcred %s %s: %v", e.method, e.path, err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode != http.StatusUnauthorized && resp2.StatusCode != http.StatusForbidden {
			t.Fatalf("badcred %s %s = %d, want 401/403", e.method, e.path, resp2.StatusCode)
		}
	}
}

// --- small helpers for ACL inspection in list responses -----------------------

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// aclBucketsFor extracts the flattened set of bucket names across all ACL rules
// of the user with the given accessKeyID in a /api/users list response.
func aclBucketsFor(t *testing.T, listBody []byte, accessKeyID string) []string {
	t.Helper()
	var users []struct {
		AccessKeyID string `json:"accessKeyID"`
		ACL         []struct {
			Buckets []string `json:"buckets"`
		} `json:"acl"`
	}
	if err := json.Unmarshal(listBody, &users); err != nil {
		t.Fatalf("decode user list: %v body=%s", err, listBody)
	}
	for _, u := range users {
		if u.AccessKeyID != accessKeyID {
			continue
		}
		var out []string
		for _, rule := range u.ACL {
			out = append(out, rule.Buckets...)
		}
		return out
	}
	return nil
}
