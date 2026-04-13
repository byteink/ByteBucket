package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the Gin context key under which the per-request identifier
// is stored. Kept unexported so handlers funnel access through RequestID,
// which applies a safe fallback when the middleware has not run yet.
const requestIDKey = "requestID"

// requestIDHeader is the S3-native response header carrying the per-request
// identifier. Using the AWS wire name (rather than X-Request-ID) keeps
// clients and log pipelines that already grep for S3 behaviour working.
const requestIDHeader = "x-amz-request-id"

// RequestIDMiddleware assigns a UUID v4 to every incoming request, publishes
// it on the Gin context and echoes it back as x-amz-request-id. It must be
// installed before auth so error responses produced inside auth carry a real
// ID — otherwise the operator has nothing to correlate a 4xx against.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := uuid.NewString()
		c.Set(requestIDKey, id)
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

// RequestID returns the per-request identifier previously set by
// RequestIDMiddleware. If the middleware has not run (tests, or a surface
// that forgot to install it), an empty string is returned rather than
// panicking — error paths must never depend on successful middleware wiring
// to emit a body.
func RequestID(c *gin.Context) string {
	v, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	id, ok := v.(string)
	if !ok {
		return ""
	}
	return id
}
