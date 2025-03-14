package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// S3ErrorResponse represents a typical S3 error response.
type S3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestId string   `xml:"RequestId"`
}

func abortWithError(c *gin.Context, status int, code, message string) {
	c.Header("Content-Type", "application/xml")
	c.AbortWithStatus(status)
	c.XML(status, S3ErrorResponse{
		Code:      code,
		Message:   message,
		RequestId: "dummy-request-id",
	})
}

// AuthMiddleware validates incoming requests using AWS Signature Version 4.
// It fully parses the Authorization header, builds the canonical request,
// computes the expected signature, and compares it to the provided signature.
func AuthMiddleware(c *gin.Context) {
	// Get the Authorization header.
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing Authorization header")
		return
	}
	const prefix = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(authHeader, prefix) {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Authorization header format")
		return
	}
	// Remove the prefix.
	authParamsStr := strings.TrimPrefix(authHeader, prefix)
	// Parse the parameters from the header (comma-separated key=value pairs).
	authParams := make(map[string]string)
	for _, param := range strings.Split(authParamsStr, ",") {
		kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
		if len(kv) == 2 {
			authParams[kv[0]] = kv[1]
		}
	}
	credential, ok := authParams["Credential"]
	if !ok {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing Credential in Authorization header")
		return
	}
	signedHeaders, ok := authParams["SignedHeaders"]
	if !ok {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing SignedHeaders in Authorization header")
		return
	}
	signatureProvided, ok := authParams["Signature"]
	if !ok {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing Signature in Authorization header")
		return
	}

	// Credential should be in the form: ACCESSKEY/20230314/region/s3/aws4_request
	credParts := strings.Split(credential, "/")
	if len(credParts) != 5 {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential format")
		return
	}
	accessKey := credParts[0]
	date := credParts[1] // YYYYMMDD
	region := credParts[2]
	service := credParts[3]
	terminal := credParts[4]
	if terminal != "aws4_request" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential terminal")
		return
	}

	// Get the X-Amz-Date header.
	amzDate := c.GetHeader("X-Amz-Date")
	if amzDate == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Date header")
		return
	}
	// Validate that the first 8 characters of X-Amz-Date match the Credential date.
	if len(amzDate) < 8 || amzDate[:8] != date {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Date mismatch in X-Amz-Date header")
		return
	}

	// Get the payload hash from the header.
	payloadHash := c.GetHeader("X-Amz-Content-Sha256")
	if payloadHash == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Content-Sha256 header")
		return
	}

	// Build the canonical request.
	canonicalRequest, err := buildCanonicalRequest(c, signedHeaders, payloadHash)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error building canonical request")
		return
	}
	hashedCanonicalRequest := hashSHA256(canonicalRequest)
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s", amzDate, credentialScope, hashedCanonicalRequest)

	// Retrieve the user's secret key.
	user, err := storage.GetUser(accessKey)
	if err != nil {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "User not found")
		return
	}
	secret, err := storage.Decrypt(user.EncryptedSecret)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error decrypting secret")
		return
	}

	// Derive the signing key and compute the expected signature.
	signingKey := getSigningKey("AWS4"+secret, date, region, service)
	expectedSignature := hex.EncodeToString(hmacSHA256([]byte(stringToSign), signingKey))
	if expectedSignature != signatureProvided {
		abortWithError(c, http.StatusUnauthorized, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided")
		return
	}

	// Optionally check that the request timestamp is within a valid time window (e.g. 15 minutes).
	t, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid X-Amz-Date format")
		return
	}
	now := time.Now().UTC()
	if now.Sub(t) > 15*time.Minute || t.Sub(now) > 15*time.Minute {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Request timestamp expired")
		return
	}

	// Signature verified; proceed to the next handler.
	c.Next()
}

// buildCanonicalRequest constructs the canonical request string according to AWS SigV4 specifications.
func buildCanonicalRequest(c *gin.Context, signedHeaders string, payloadHash string) (string, error) {
	method := c.Request.Method
	canonicalURI := c.Request.URL.EscapedPath()

	// Build canonical query string by sorting query parameters.
	query := c.Request.URL.Query()
	var queryParts []string
	for key, values := range query {
		for _, value := range values {
			// URL-encode key and value.
			queryParts = append(queryParts, url.QueryEscape(strings.ToLower(key))+"="+url.QueryEscape(value))
		}
	}
	sort.Strings(queryParts)
	canonicalQuery := strings.Join(queryParts, "&")

	// Build canonical headers from the signed headers.
	headersToSign := strings.Split(signedHeaders, ";")
	var canonicalHeaders string
	for _, headerName := range headersToSign {
		headerName = strings.ToLower(headerName)
		headerValues := c.Request.Header[http.CanonicalHeaderKey(headerName)]
		if len(headerValues) == 0 {
			continue
		}
		// Join multiple values with a comma and trim spaces.
		value := strings.TrimSpace(strings.Join(headerValues, ","))
		canonicalHeaders += headerName + ":" + value + "\n"
	}

	// Construct the canonical request.
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s", method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash)
	return canonicalRequest, nil
}

// hashSHA256 returns the SHA256 hash of the given data as a hexadecimal string.
func hashSHA256(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// hmacSHA256 computes HMAC-SHA256 of the given message using the given key.
func hmacSHA256(message, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// getSigningKey derives the AWS SigV4 signing key.
func getSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte(date), []byte(secret))
	kRegion := hmacSHA256([]byte(region), kDate)
	kService := hmacSHA256([]byte(service), kRegion)
	kSigning := hmacSHA256([]byte("aws4_request"), kService)
	return kSigning
}
