package middleware

import (
	"log/slog"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// Log returns a Gin middleware that emits one structured log line per request
// after the handler chain completes. It replaces gin.Logger()'s ASCII-art
// output so log aggregators can parse fields without regex and operators can
// grep/filter on first-class keys.
//
// Design notes:
//   - path is c.FullPath() (the registered route template, e.g.
//     "/s3/:bucket/*objectKey") rather than c.Request.URL.Path. Using the raw
//     path would explode label cardinality the moment a client walks a
//     large keyspace; the template stays bounded by the route table.
//   - Query strings are deliberately dropped. SigV4 presigned URLs carry the
//     signature in the query string; logging it would leak credentials into
//     any downstream aggregator.
//   - Headers and bodies are never logged. Request IDs already provide
//     correlation; operators wanting payloads can capture at the proxy.
//   - Level is chosen from status: 5xx errors, 4xx warns, everything else
//     informational. 3xx is rare on this surface and treated as success.
//   - If a handler recorded errors via c.Errors (Gin's native mechanism), the
//     concatenated message is attached on an error field so a single log line
//     per request remains the contract.
func Log() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		path := c.FullPath()
		if path == "" {
			// No matching route — emit the literal request path so 404s are
			// still visible, but still stripped of query string.
			path = c.Request.URL.Path
		}

		attrs := []slog.Attr{
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
			slog.String("remote_ip", c.ClientIP()),
			slog.Int64("bytes_in", c.Request.ContentLength),
			slog.Int("bytes_out", c.Writer.Size()),
		}
		if id := RequestID(c); id != "" {
			attrs = append(attrs, slog.String("request_id", id))
		}
		if v, ok := c.Get("authMethod"); ok {
			if s, ok := v.(string); ok && s != "" {
				attrs = append(attrs, slog.String("auth_method", s))
			}
		}
		if v, ok := c.Get("user"); ok {
			if u, ok := v.(*storage.User); ok && u != nil && u.AccessKeyID != "" {
				attrs = append(attrs, slog.String("user_access_key", u.AccessKeyID))
			}
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("error", c.Errors.String()))
		}

		level := levelFor(status)
		slog.LogAttrs(c.Request.Context(), level, "http_request", attrs...)
	}
}

// levelFor maps HTTP status to slog level. 5xx is always ERROR so alerting
// pipelines catch server faults; 4xx is WARN so client misuse is visible but
// does not page; everything else is INFO.
func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
