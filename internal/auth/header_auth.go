package auth

import (
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

type headerCredentials struct {
	accessKey, date, region, service string
	signedHeaders, signature         string
}

func processHeaderAuth(c *gin.Context, authHeader string) {
	credentials, ok := parseHeaderCredentials(c, authHeader)
	if !ok {
		return
	}
	amzDate, ok := headerDate(c, credentials.date)
	if !ok {
		return
	}
	payloadHash := c.GetHeader("X-Amz-Content-Sha256")
	if payloadHash == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Content-Sha256 header")
		return
	}
	user, ok := authenticateHeader(c, credentials, amzDate, payloadHash)
	if !ok || !validateTimestamp(c, amzDate) || !publishSignedIdentity(c, user) {
		return
	}
	serveVerifiedPayload(c, payloadHash)
}

func parseHeaderCredentials(c *gin.Context, authHeader string) (headerCredentials, bool) {
	params := make(map[string]string)
	for _, param := range strings.Split(strings.TrimPrefix(authHeader, "AWS4-HMAC-SHA256 "), ",") {
		kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = kv[1]
		}
	}
	for _, name := range []string{"Credential", "SignedHeaders", "Signature"} {
		if _, ok := params[name]; !ok {
			abortWithError(c, http.StatusUnauthorized, "AccessDenied", fmt.Sprintf("Missing %s in Authorization header", name))
			return headerCredentials{}, false
		}
	}
	parts := strings.Split(params["Credential"], "/")
	if len(parts) != 5 {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential format")
		return headerCredentials{}, false
	}
	if parts[4] != "aws4_request" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Invalid Credential terminal")
		return headerCredentials{}, false
	}
	return headerCredentials{
		accessKey: parts[0], date: parts[1], region: parts[2], service: parts[3],
		signedHeaders: params["SignedHeaders"], signature: params["Signature"],
	}, true
}

func headerDate(c *gin.Context, date string) (string, bool) {
	amzDate := c.GetHeader("X-Amz-Date")
	if amzDate == "" {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Missing X-Amz-Date header")
		return "", false
	}
	if len(amzDate) < 8 || amzDate[:8] != date {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "Date mismatch in X-Amz-Date header")
		return "", false
	}
	return amzDate, true
}

func authenticateHeader(c *gin.Context, credentials headerCredentials, amzDate, payloadHash string) (*storage.User, bool) {
	canonical, err := buildCanonicalRequest(c, credentials.signedHeaders, payloadHash, nil)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error building canonical request")
		return nil, false
	}
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", credentials.date, credentials.region, credentials.service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s", amzDate, scope, hashSHA256(canonical))
	user, err := storage.GetUser(credentials.accessKey)
	if err != nil {
		abortWithError(c, http.StatusUnauthorized, "AccessDenied", "User not found")
		return nil, false
	}
	secret, err := storage.Decrypt(user.EncryptedSecret)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error decrypting secret")
		return nil, false
	}
	key := getSigningKey("AWS4"+secret, credentials.date, credentials.region, credentials.service)
	expected := hmacSHA256([]byte(stringToSign), key)
	provided, err := hex.DecodeString(credentials.signature)
	if err != nil || !hmac.Equal(expected, provided) {
		abortWithError(c, http.StatusUnauthorized, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided")
		return nil, false
	}
	return user, true
}
