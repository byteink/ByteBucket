package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// ErrorCodeContextKey is where respondError publishes the S3 error code, so the
// access-log capture can record what failed (NoSuchKey, AccessDenied, ...)
// without re-deriving it. It lives in this package because the capture
// middleware reads it while the handlers package (which imports middleware)
// sets it — the reverse import would be a cycle.
const ErrorCodeContextKey = "s3ErrorCode"

// AccessLog records one data-plane access event per request into the unified
// event log. Mounted only on the S3 data plane (the whole :9000 surface and the
// admin /api/s3 group), so admin management calls, SPA assets, /metrics and
// /health never appear in it. When data-plane logging is disabled the only cost
// on the hot path is a single atomic load, so leaving it mounted is free.
//
// The event is enqueued non-blocking for the async batch writer, and also
// emitted to stdout enriched with op/bucket/key. The generic request log
// (Log()) carries only the bounded route template, so this "access" line is
// what makes "who accessed object X" greppable in container logs.
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		if !storage.AccessLogEnabled() {
			return
		}
		switch c.FullPath() {
		case "", "/health", "/favicon.ico":
			// Unmatched route, orchestrator probe, or browser favicon probe —
			// not object traffic.
			return
		}
		e := storage.Event{
			TimeUnixNano: start.UnixNano(),
			Category:     storage.EventData,
			Actor:        accessActor(c),
			Op:           s3Operation(c),
			Bucket:       c.Param("bucket"),
			Key:          strings.TrimPrefix(c.Param("objectKey"), "/"),
			Status:       c.Writer.Status(),
			ErrorCode:    contextString(c, ErrorCodeContextKey),
			ClientIP:     c.ClientIP(),
			BytesIn:      c.Request.ContentLength,
			BytesOut:     int64(c.Writer.Size()),
			DurationMs:   float64(time.Since(start).Microseconds()) / 1000.0,
			UserAgent:    c.Request.UserAgent(),
		}
		storage.EnqueueEvent(e)
		slog.LogAttrs(c.Request.Context(), slog.LevelInfo, "access",
			slog.String("op", e.Op),
			slog.String("bucket", e.Bucket),
			slog.String("key", e.Key),
			slog.Int("status", e.Status),
			slog.String("actor", e.Actor),
			slog.String("client_ip", e.ClientIP),
			slog.Int64("bytes_out", e.BytesOut),
		)
	}
}

// accessActor returns the authenticated access key, or "anonymous" for an
// unauthenticated public-read GET/HEAD.
func accessActor(c *gin.Context) string {
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok && u != nil && u.AccessKeyID != "" {
			return u.AccessKeyID
		}
	}
	return "anonymous"
}

func contextString(c *gin.Context, key string) string {
	if v, ok := c.Get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// s3Operation classifies a request into an S3 operation name for the access
// log. It reads the method, the bucket/key params, and the *presence* of
// subresource query keys (never their values — presigned signatures live in
// query values and must not be read here). It mirrors the dispatch tables in
// storage_routes.go; an unmapped shape falls back to the HTTP method rather
// than guessing, so the label is always a safe best-effort.
func s3Operation(c *gin.Context) string {
	key := strings.TrimPrefix(c.Param("objectKey"), "/")
	q := c.Request.URL.Query()
	has := func(k string) bool { _, ok := q[k]; return ok }

	switch c.Request.Method {
	case http.MethodGet:
		switch {
		case has("acl"):
			return "GetAcl"
		case has("tagging"):
			return "GetObjectTagging"
		case has("cors"):
			return "GetBucketCors"
		case has("uploads"):
			return "ListMultipartUploads"
		case has("presign"):
			return "PresignObject"
		case q.Get("uploadId") != "":
			return "ListParts"
		case key == "" && c.Param("bucket") == "":
			return "ListBuckets"
		case key == "":
			return "ListObjects"
		default:
			return "GetObject"
		}
	case http.MethodPut:
		switch {
		case has("acl"):
			return "PutAcl"
		case has("tagging"):
			return "PutObjectTagging"
		case has("cors"):
			return "PutBucketCors"
		case q.Get("uploadId") != "" && q.Get("partNumber") != "":
			return "UploadPart"
		case c.GetHeader("x-amz-copy-source") != "":
			return "CopyObject"
		case key == "":
			return "CreateBucket"
		default:
			return "PutObject"
		}
	case http.MethodDelete:
		switch {
		case has("tagging"):
			return "DeleteObjectTagging"
		case has("cors"):
			return "DeleteBucketCors"
		case has("delete"):
			return "DeleteObjects"
		case q.Get("uploadId") != "":
			return "AbortMultipartUpload"
		case key == "":
			return "DeleteBucket"
		default:
			return "DeleteObject"
		}
	case http.MethodPost:
		switch {
		case has("delete"):
			return "DeleteObjects"
		case has("uploads"):
			return "CreateMultipartUpload"
		case q.Get("uploadId") != "":
			return "CompleteMultipartUpload"
		default:
			return c.Request.Method
		}
	case http.MethodHead:
		if key == "" {
			return "HeadBucket"
		}
		return "HeadObject"
	default:
		return c.Request.Method
	}
}
