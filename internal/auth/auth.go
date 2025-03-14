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

// AdminAuthMiddleware authenticates admin requests by extracting credentials from headers,
// finding the corresponding user in storage, decrypting the stored secret, and verifying
// that the user has admin privileges (i.e. an ACL rule with Effect "Allow" and both Buckets
// and Actions set to "*").
func AdminAuthMiddleware(c *gin.Context) {
	// Extract credentials from headers.
	accessKey := c.GetHeader("X-Admin-AccessKey")
	providedSecret := c.GetHeader("X-Admin-Secret")
	if accessKey == "" || providedSecret == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Missing admin credentials"})
		return
	}

	// Retrieve the user from storage using the accessKey.
	user, err := storage.GetUser(accessKey)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
		return
	}

	// Decrypt the stored secret.
	storedSecret, err := storage.Decrypt(user.EncryptedSecret)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Error decrypting secret"})
		return
	}

	// Verify that the provided secret matches the stored (decrypted) secret.
	if providedSecret != storedSecret {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid admin secret"})
		return
	}

	// Check admin privileges.
	// Here we define a user as admin if they have an ACL rule with Effect "Allow" and both
	// Buckets and Actions set to "*".
	isAdmin := false
	for _, rule := range user.ACL {
		if strings.EqualFold(rule.Effect, "Allow") {
			hasAllBuckets := false
			hasAllActions := false
			for _, bucket := range rule.Buckets {
				if bucket == "*" {
					hasAllBuckets = true
					break
				}
			}
			for _, action := range rule.Actions {
				if action == "*" {
					hasAllActions = true
					break
				}
			}
			if hasAllBuckets && hasAllActions {
				isAdmin = true
				break
			}
		}
	}

	if !isAdmin {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User does not have admin privileges"})
		return
	}

	// All checks passed, continue to the next handler.
	c.Next()
}

// AuthMiddleware validates incoming requests using AWS Signature Version 4.
// It supports both standard header-based authentication and presigned URL (query parameter)
// authentication. For presigned URLs, the required X-Amz-* query parameters are used.
func AuthMiddleware(c *gin.Context) {
	// First, check if an Authorization header is provided.
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		// Process standard header-based signature
		if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256 ") {
			abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Authorization header format")
			return
		}
		processHeaderAuth(c, authHeader)
		return
	}

	// If no Authorization header, check for presigned URL query parameters.
	if c.Query("X-Amz-Algorithm") != "" {
		processPresignedAuth(c)
		return
	}

	abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing authentication information")
}

// processHeaderAuth handles signature validation when an Authorization header is provided.
func processHeaderAuth(c *gin.Context, authHeader string) {
	const prefix = "AWS4-HMAC-SHA256 "
	authParamsStr := strings.TrimPrefix(authHeader, prefix)
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

	amzDate := c.GetHeader("X-Amz-Date")
	if amzDate == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Date header")
		return
	}
	if len(amzDate) < 8 || amzDate[:8] != date {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Date mismatch in X-Amz-Date header")
		return
	}
	payloadHash := c.GetHeader("X-Amz-Content-Sha256")
	if payloadHash == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Content-Sha256 header")
		return
	}

	canonicalRequest, err := buildCanonicalRequest(c, signedHeaders, payloadHash, nil)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error building canonical request")
		return
	}
	hashedCanonicalRequest := hashSHA256(canonicalRequest)
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s", amzDate, credentialScope, hashedCanonicalRequest)

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

	signingKey := getSigningKey("AWS4"+secret, date, region, service)
	expectedSignature := hex.EncodeToString(hmacSHA256([]byte(stringToSign), signingKey))
	if expectedSignature != signatureProvided {
		abortWithError(c, http.StatusUnauthorized, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided")
		return
	}

	validateTimestamp(c, amzDate)

	// Check if the user is allowed to perform the action
	if !isUserAllowed(user, c.Request.Method, c.Param("bucket")) {
		abortWithError(c, http.StatusForbidden, "AccessDenied", "User does not have permission to perform this action")
		return
	}

	c.Next()
}

func isUserAllowed(user *storage.User, method, bucket string) bool {
	action := getActionFromMethod(method)
	for _, rule := range user.ACL {
		if strings.EqualFold(rule.Effect, "Allow") {
			if isBucketAllowed(rule.Buckets, bucket) && isActionAllowed(rule.Actions, action) {
				return true
			}
		}
	}
	return false
}

func getActionFromMethod(method string) string {
	switch method {
	case http.MethodGet:
		return "s3:GetObject"
	case http.MethodPut:
		return "s3:PutObject"
	case http.MethodDelete:
		return "s3:DeleteObject"
	default:
		return ""
	}
}

func isBucketAllowed(buckets []string, bucket string) bool {
	for _, b := range buckets {
		if b == "*" || b == bucket {
			return true
		}
	}
	return false
}

func isActionAllowed(actions []string, action string) bool {
	for _, a := range actions {
		if a == "*" || a == action {
			return true
		}
	}
	return false
}

// processPresignedAuth handles signature validation when using presigned URL query parameters.
func processPresignedAuth(c *gin.Context) {
	// Extract required query parameters.
	amzAlgorithm := c.Query("X-Amz-Algorithm")
	credential := c.Query("X-Amz-Credential")
	amzDate := c.Query("X-Amz-Date")
	expires := c.Query("X-Amz-Expires")
	signatureProvided := c.Query("X-Amz-Signature")
	signedHeaders := c.Query("X-Amz-SignedHeaders")
	payloadHash := c.Query("X-Amz-Content-Sha256")
	// Note: Some presigned URLs use "UNSIGNED-PAYLOAD" for payload hash.
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}

	if amzAlgorithm == "" || credential == "" || amzDate == "" || expires == "" || signatureProvided == "" || signedHeaders == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing required presigned URL query parameters")
		return
	}

	credParts := strings.Split(credential, "/")
	if len(credParts) != 5 {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential format in query parameters")
		return
	}
	accessKey := credParts[0]
	date := credParts[1] // YYYYMMDD
	region := credParts[2]
	service := credParts[3]
	terminal := credParts[4]
	if terminal != "aws4_request" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential terminal in query parameters")
		return
	}

	// For presigned URLs, the canonical query string must be built from the URL query parameters
	// but without the X-Amz-Signature.
	// Make a copy of the query parameters, and remove "X-Amz-Signature".
	queryVals := c.Request.URL.Query()
	queryVals.Del("X-Amz-Signature")
	// Build canonical query string using these parameters.
	canonicalQuery, err := buildCanonicalQuery(queryVals)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error building canonical query string")
		return
	}

	// Build canonical headers using the signed headers.
	canonicalHeaders, err := buildCanonicalHeaders(c, signedHeaders)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error building canonical headers")
		return
	}

	// Construct the canonical request.
	// For presigned URLs, the method, URI, canonical query, canonical headers, signed headers, and payload hash.
	method := c.Request.Method
	canonicalURI := c.Request.URL.EscapedPath()
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)
	hashedCanonicalRequest := hashSHA256(canonicalRequest)
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s", amzDate, credentialScope, hashedCanonicalRequest)

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

	signingKey := getSigningKey("AWS4"+secret, date, region, service)
	expectedSignature := hex.EncodeToString(hmacSHA256([]byte(stringToSign), signingKey))
	if expectedSignature != signatureProvided {
		abortWithError(c, http.StatusUnauthorized, "SignatureDoesNotMatch", "The presigned URL signature does not match")
		return
	}

	validateTimestamp(c, amzDate)
	c.Next()
}

// buildCanonicalRequest constructs the canonical request string according to AWS SigV4 specifications.
// If extraQuery is provided, it will be used as the canonical query string; otherwise, the request's query is used.
func buildCanonicalRequest(c *gin.Context, signedHeaders string, payloadHash string, extraQuery url.Values) (string, error) {
	method := c.Request.Method
	canonicalURI := c.Request.URL.EscapedPath()

	var canonicalQuery string
	if extraQuery != nil {
		q, err := buildCanonicalQuery(extraQuery)
		if err != nil {
			return "", err
		}
		canonicalQuery = q
	} else {
		query := c.Request.URL.Query()
		q, err := buildCanonicalQuery(query)
		if err != nil {
			return "", err
		}
		canonicalQuery = q
	}

	canonicalHeaders, err := buildCanonicalHeaders(c, signedHeaders)
	if err != nil {
		return "", err
	}

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)
	return canonicalRequest, nil
}

// buildCanonicalQuery builds the canonical query string from given URL values.
func buildCanonicalQuery(query url.Values) (string, error) {
	var queryParts []string
	for key, values := range query {
		for _, value := range values {
			// URL-encode key and value without lowercasing the key.
			queryParts = append(queryParts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	sort.Strings(queryParts)
	return strings.Join(queryParts, "&"), nil
}

// buildCanonicalHeaders builds canonical headers for the given signed headers.
func buildCanonicalHeaders(c *gin.Context, signedHeaders string) (string, error) {
	headerNames := strings.Split(signedHeaders, ";")
	var headerLines []string
	for _, headerName := range headerNames {
		headerName = strings.TrimSpace(strings.ToLower(headerName))
		var value string
		if headerName == "host" {
			value = c.Request.Host
		} else {
			values := c.Request.Header[http.CanonicalHeaderKey(headerName)]
			if len(values) == 0 {
				value = ""
			} else {
				value = strings.Join(values, ",")
				value = strings.Join(strings.Fields(value), " ")
			}
		}
		headerLines = append(headerLines, fmt.Sprintf("%s:%s\n", headerName, value))
	}
	return strings.Join(headerLines, ""), nil
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

// validateTimestamp checks that the provided amzDate is within 15 minutes of the current time.
func validateTimestamp(c *gin.Context, amzDate string) {
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
}
