package middleware

import (
	"encoding/xml"
	"net/http"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// ValidateNames is a single chokepoint that rejects malformed bucket names
// and object keys before any handler runs. Centralising the check here
// means a future route addition cannot accidentally skip validation — the
// router applies this middleware once at registration time.
//
// The middleware is deliberately *not* placed in package auth because
// validation is independent of authentication: an attacker probing with
// no credentials should hit the same name rules a signed admin would.
// Failing earlier also keeps the auth path's anonymous-read branch from
// constructing filesystem paths on untrusted input.
func ValidateNames() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Bucket-level routes use ":bucket"; object-level routes also bind
		// "*objectKey". An empty bucket param is the ListBuckets case
		// (mounted at "/"), which the validator must leave alone.
		bucket := c.Param("bucket")
		if bucket != "" {
			if err := storage.ValidateBucketName(bucket); err != nil {
				abortInvalid(c, "InvalidBucketName",
					"Bucket name must be 3-63 chars, lowercase alphanumeric and hyphens, starting and ending alphanumeric")
				return
			}
		}

		// Wildcard object key. Gin's *objectKey captures the entire
		// trailing path including a leading "/", which is the wire shape
		// AWS uses but not the on-disk key — strip the slash before
		// validating and re-publish the cleaned form for handlers.
		rawKey := c.Param("objectKey")
		trimmed := strings.TrimPrefix(rawKey, "/")
		if trimmed != "" {
			cleaned, err := storage.ValidateObjectKey(trimmed)
			if err != nil {
				abortInvalid(c, "InvalidArgument",
					"Object key must not contain path traversal, NUL bytes, or reserved sidecar names")
				return
			}
			c.Set("cleanedObjectKey", cleaned)
		}
		c.Next()
	}
}

// errBody is the S3 XML error shape — kept local to this package so the
// validator does not need to import handlers, which would form a cycle.
type errBody struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestId string   `xml:"RequestId"`
}

// abortInvalid writes a protocol-matched error body. JSON on the admin
// surface (path starts with /api or Accept asks for it); S3 XML otherwise.
func abortInvalid(c *gin.Context, code, message string) {
	if strings.HasPrefix(c.Request.URL.Path, "/api") ||
		strings.Contains(c.GetHeader("Accept"), "application/json") {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":      code,
			"message":   message,
			"requestId": RequestID(c),
		})
		return
	}
	c.Header("Content-Type", "application/xml")
	c.AbortWithStatus(http.StatusBadRequest)
	c.XML(http.StatusBadRequest, errBody{Code: code, Message: message, RequestId: RequestID(c)})
}
