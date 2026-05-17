package auth

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PresignGetURL builds a SigV4-presigned URL that downloads an object from
// the storage surface. The signature is computed against the exact canonical
// request that processPresignedAuth validates, so a URL produced here is
// guaranteed to round-trip through the existing auth path without any
// special-casing.
//
// baseURL is the externally-reachable storage origin (PUBLIC_BASE_URL).
// Region and service are pinned to "us-east-1"/"s3" to match what the AWS
// SDK uses by default — ByteBucket has no regional routing, but signing
// requires *some* value and clients commonly hardcode that pair.
func PresignGetURL(accessKey, secret, baseURL, bucket, objectKey string, expires time.Duration) (string, error) {
	if accessKey == "" || secret == "" {
		return "", fmt.Errorf("presign: missing credentials")
	}
	if bucket == "" || objectKey == "" {
		return "", fmt.Errorf("presign: missing bucket/key")
	}
	if expires <= 0 {
		return "", fmt.Errorf("presign: expires must be positive")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	// Per-segment escaping so a literal "/" inside the key is preserved as
	// a path separator while everything else gets percent-encoded. This is
	// what SDKs do and what the verifier on the other end expects.
	segments := strings.Split(objectKey, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	u.Path = "/" + url.PathEscape(bucket) + "/" + strings.Join(segments, "/")

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := amzDate[:8]
	region := "us-east-1"
	service := "s3"
	expiresSec := int(expires.Seconds())

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", fmt.Sprintf("%s/%s/%s/%s/aws4_request", accessKey, date, region, service))
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(expiresSec))
	q.Set("X-Amz-SignedHeaders", "host")

	canonicalQuery, err := buildCanonicalQuery(q)
	if err != nil {
		return "", err
	}
	canonicalHeaders := "host:" + u.Host + "\n"
	// Presigned URLs sign UNSIGNED-PAYLOAD by default — the body bytes are
	// not known at sign time and the integrity check is delegated to TLS.
	payloadHash := "UNSIGNED-PAYLOAD"

	canonicalRequest := strings.Join([]string{
		"GET",
		u.EscapedPath(),
		canonicalQuery,
		canonicalHeaders,
		"host",
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hashSHA256(canonicalRequest),
	}, "\n")

	signingKey := getSigningKey("AWS4"+secret, date, region, service)
	sig := hex.EncodeToString(hmacSHA256([]byte(stringToSign), signingKey))
	q.Set("X-Amz-Signature", sig)

	u.RawQuery = q.Encode()
	return u.String(), nil
}
