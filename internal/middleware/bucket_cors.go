package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// BucketCORSMiddleware resolves CORS headers from the per-bucket configuration
// stored in the bucket directory. It runs before authentication so OPTIONS
// preflights (which browsers send without SigV4) get answered. Buckets
// without a CORS config receive no Access-Control-* headers — that is the
// S3 contract: a browser request to a CORS-less bucket must fail CORS in the
// browser, not be silently waved through by the server.
func BucketCORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		bucket := bucketFromPath(c.Request.URL.Path)

		// Non-browser request (no Origin): let downstream handlers run, no
		// CORS work needed.
		if origin == "" {
			c.Next()
			return
		}

		// OPTIONS requests that never reach a bucket cannot be evaluated;
		// S3 answers 403 when a browser tries CORS-preflighting the service
		// endpoint itself.
		if bucket == "" {
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		cfg, err := storage.GetBucketCORS(bucket)
		if err != nil {
			// No config or read error: emit no CORS headers. For preflight,
			// 403 signals the browser to abort; that matches S3 behaviour
			// for a bucket whose CORS config is absent.
			if errors.Is(err, storage.ErrNoSuchCORSConfiguration) && c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		reqMethod := c.Request.Method
		if reqMethod == http.MethodOptions {
			reqMethod = c.Request.Header.Get("Access-Control-Request-Method")
		}
		reqHeaders := c.Request.Header.Get("Access-Control-Request-Headers")

		rule, ok := matchCORSRule(cfg.CORSRules, origin, reqMethod, reqHeaders)
		if !ok {
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		applyCORSHeaders(c, rule, origin)
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// bucketFromPath extracts the first non-empty path segment after the root.
// The middleware runs before Gin matches a route, so c.Param is unavailable.
func bucketFromPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

// matchCORSRule returns the first rule that matches origin + method + headers,
// mirroring the AWS evaluation order. The returned bool reports whether any
// rule matched; callers use it to decide whether to emit CORS headers.
func matchCORSRule(rules []storage.BucketCORSRule, origin, method, reqHeaders string) (storage.BucketCORSRule, bool) {
	for _, r := range rules {
		if !matchList(r.AllowedOrigins, origin) {
			continue
		}
		if method != "" && !matchList(r.AllowedMethods, method) {
			continue
		}
		if !headersAllowed(r.AllowedHeaders, reqHeaders) {
			continue
		}
		return r, true
	}
	return storage.BucketCORSRule{}, false
}

// matchList reports whether s matches any entry in allowed. S3 CORS allows a
// single "*" as a wildcard anywhere inside a pattern (e.g. "https://*.ex.com"
// or "*.ex.com"); we split on the first "*" and anchor both sides. No library
// dependency on this hot path: one allocation at most, no regex compile.
func matchList(allowed []string, s string) bool {
	for _, a := range allowed {
		if a == s {
			return true
		}
		star := strings.IndexByte(a, '*')
		if star < 0 {
			continue
		}
		prefix := a[:star]
		suffix := a[star+1:]
		if len(s) < len(prefix)+len(suffix) {
			continue
		}
		if strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// headersAllowed returns true when every header listed in the preflight's
// Access-Control-Request-Headers is covered by the rule's AllowedHeaders.
// An empty request-headers list trivially matches.
func headersAllowed(allowed []string, reqHeaders string) bool {
	if reqHeaders == "" {
		return true
	}
	for _, h := range strings.Split(reqHeaders, ",") {
		h = strings.TrimSpace(strings.ToLower(h))
		if h == "" {
			continue
		}
		found := false
		for _, a := range allowed {
			a = strings.ToLower(a)
			if a == "*" || a == h {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// applyCORSHeaders writes the ACA headers for a matched rule. The origin is
// echoed back verbatim (S3 never returns "*" with credentials) and Vary is
// set so intermediaries cache per-origin.
func applyCORSHeaders(c *gin.Context, r storage.BucketCORSRule, origin string) {
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Vary", "Origin")
	if len(r.AllowedMethods) > 0 {
		c.Header("Access-Control-Allow-Methods", strings.Join(r.AllowedMethods, ", "))
	}
	if len(r.AllowedHeaders) > 0 {
		c.Header("Access-Control-Allow-Headers", strings.Join(r.AllowedHeaders, ", "))
	}
	if len(r.ExposeHeaders) > 0 {
		c.Header("Access-Control-Expose-Headers", strings.Join(r.ExposeHeaders, ", "))
	}
	if r.MaxAgeSeconds > 0 {
		c.Header("Access-Control-Max-Age", strconv.Itoa(r.MaxAgeSeconds))
	}
}
