package tests

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- minimal SigV4 helpers duplicated locally so tests can send raw, malformed,
// or tampered requests that the AWS SDK would refuse to construct. ---

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmac256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func deriveSigningKey(secret, date, region, service string) []byte {
	k := hmac256([]byte("AWS4"+secret), []byte(date))
	k = hmac256(k, []byte(region))
	k = hmac256(k, []byte(service))
	return hmac256(k, []byte("aws4_request"))
}

type sigV4Request struct {
	method       string
	host         string
	path         string
	query        url.Values
	body         []byte
	payloadHash  string // if empty, computed from body
	accessKey    string
	secret       string
	region       string
	service      string
	now          time.Time
	extraHeaders map[string]string
}

// buildHeaderSigned returns an *http.Request that includes a full SigV4
// Authorization header.
func buildHeaderSigned(t *testing.T, target string, req sigV4Request) *http.Request {
	t.Helper()
	if req.region == "" {
		req.region = "us-east-1"
	}
	if req.service == "" {
		req.service = "s3"
	}
	if req.now.IsZero() {
		req.now = time.Now()
	}
	amzDate := req.now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	payloadHash := req.payloadHash
	if payloadHash == "" {
		payloadHash = sha256Hex(req.body)
	}

	u, err := url.Parse(target + req.path)
	if err != nil {
		t.Fatalf("url parse: %v", err)
	}
	if req.query != nil {
		u.RawQuery = req.query.Encode()
	}

	httpReq, err := http.NewRequest(req.method, u.String(), bytes.NewReader(req.body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	host := u.Host
	if req.host != "" {
		host = req.host
	}
	httpReq.Host = host
	httpReq.Header.Set("X-Amz-Date", amzDate)
	httpReq.Header.Set("X-Amz-Content-Sha256", payloadHash)
	for k, v := range req.extraHeaders {
		httpReq.Header.Set(k, v)
	}

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, payloadHash, amzDate)

	var cq string
	if req.query != nil {
		var parts []string
		for k, vs := range req.query {
			for _, v := range vs {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		sort.Strings(parts)
		cq = strings.Join(parts, "&")
	}

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.method, u.EscapedPath(), cq, canonicalHeaders, signedHeaders, payloadHash)

	credScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, req.region, req.service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credScope, sha256Hex([]byte(canonicalRequest)))
	sig := hex.EncodeToString(hmac256(deriveSigningKey(req.secret, date, req.region, req.service), []byte(stringToSign)))

	httpReq.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		req.accessKey, credScope, signedHeaders, sig,
	))
	return httpReq
}

// ensureBucket is a convenience: create a bucket via signed PUT if missing.
func ensureBucket(t *testing.T, ak, sk, bucket string) {
	t.Helper()
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPut, path: "/" + bucket,
		accessKey: ak, secret: sk,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	_ = resp.Body.Close()
}

// TestE2E_Bug1_TamperedSignatureRejected: flip one byte in the signature,
// expect 401 SignatureDoesNotMatch.
func TestE2E_Bug1_TamperedSignatureRejected(t *testing.T) {
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodGet, path: "/",
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	auth := req.Header.Get("Authorization")
	// Flip last hex char of the signature.
	last := auth[len(auth)-1]
	flipped := byte('a')
	if last == 'a' {
		flipped = 'b'
	}
	req.Header.Set("Authorization", auth[:len(auth)-1]+string(flipped))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "SignatureDoesNotMatch") {
		t.Fatalf("expected SignatureDoesNotMatch, got %s", body)
	}
}
