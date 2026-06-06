package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// etagMatches reports whether an If-Match / If-None-Match header value matches
// the object's current ETag. "*" matches any existing object; a comma list
// matches if any member equals the ETag. A weak "W/" prefix is tolerated and
// compared by value, which is sufficient for the single-validator ETags we
// emit. etag is the canonical quoted form (e.g. "abc").
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		return true
	}
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cand), "W/"))
		if cand == etag {
			return true
		}
	}
	return false
}

// evaluateGetConditional applies the RFC 7232 precondition order to a read.
// It returns the HTTP status the caller must emit and handled=true when a
// precondition short-circuits the response; (0, false) means serve normally.
// 304 carries no body; 412 is surfaced as a PreconditionFailed error by the
// caller. gin defers WriteHeader, so the caller must flush an empty 304 with
// WriteHeaderNow — ServeContent cannot be relied on through the gin writer.
func evaluateGetConditional(c *gin.Context, etag string, modTime time.Time) (int, bool) {
	// Step 1: If-Match, else If-Unmodified-Since.
	if im := c.GetHeader("If-Match"); im != "" {
		if !etagMatches(im, etag) {
			return http.StatusPreconditionFailed, true
		}
	} else if ius := c.GetHeader("If-Unmodified-Since"); ius != "" {
		if t, err := http.ParseTime(ius); err == nil && modTime.After(t) {
			return http.StatusPreconditionFailed, true
		}
	}

	// Step 2: If-None-Match, else If-Modified-Since.
	if inm := c.GetHeader("If-None-Match"); inm != "" {
		if etagMatches(inm, etag) {
			return http.StatusNotModified, true
		}
	} else if ims := c.GetHeader("If-Modified-Since"); ims != "" {
		// Truncate to seconds: HTTP-date has no sub-second precision, so a
		// modTime in the same second as the header must count as not modified.
		if t, err := http.ParseTime(ims); err == nil && !modTime.Truncate(time.Second).After(t) {
			return http.StatusNotModified, true
		}
	}
	return 0, false
}

// applyGetConditional evaluates read preconditions and writes the short-circuit
// response (304 empty, or 412 error) when one fires. Returns true when it has
// written a response and the caller must stop. The four precondition headers
// are stripped afterwards so the downstream ServeContent does not re-evaluate
// them (its deferred 304 would be swallowed by gin and surface as 200).
func applyGetConditional(c *gin.Context, etag string, modTime time.Time) bool {
	status, handled := evaluateGetConditional(c, etag, modTime)
	stripConditionalHeaders(c)
	if !handled {
		return false
	}
	if status == http.StatusNotModified {
		c.Header("ETag", etag)
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return true
	}
	respondError(c, http.StatusPreconditionFailed, "PreconditionFailed", "At least one precondition failed")
	return true
}

// evaluatePutConditional implements optimistic-concurrency preconditions on a
// write. If-None-Match: * creates only when the object is absent; If-Match: tag
// overwrites only when the current ETag matches. A mismatch is 412. (0, false)
// means proceed with the write. dstPath is the resolved on-disk object path.
func evaluatePutConditional(c *gin.Context, dstPath string) (int, bool) {
	ifMatch := c.GetHeader("If-Match")
	ifNoneMatch := c.GetHeader("If-None-Match")
	if ifMatch == "" && ifNoneMatch == "" {
		return 0, false
	}

	info, statErr := os.Stat(dstPath)
	exists := statErr == nil && !info.IsDir()

	if ifNoneMatch != "" {
		if ifNoneMatch == "*" {
			if exists {
				return http.StatusPreconditionFailed, true
			}
		} else if exists {
			if etag, err := loadOrBackfillETag(dstPath); err == nil && etagMatches(ifNoneMatch, etag) {
				return http.StatusPreconditionFailed, true
			}
		}
	}

	if ifMatch != "" {
		if !exists {
			return http.StatusPreconditionFailed, true
		}
		etag, err := loadOrBackfillETag(dstPath)
		if err != nil {
			return http.StatusInternalServerError, true
		}
		if !etagMatches(ifMatch, etag) {
			return http.StatusPreconditionFailed, true
		}
	}
	return 0, false
}

// stripConditionalHeaders removes the read precondition headers so a downstream
// handler (ServeContent) cannot act on them a second time.
func stripConditionalHeaders(c *gin.Context) {
	for _, h := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		c.Request.Header.Del(h)
	}
}
