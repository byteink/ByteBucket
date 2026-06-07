package handlers

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ByteBucket/internal/auth"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// presignMaxExpires caps the lifetime of a presigned URL at the AWS-defined
// SigV4 maximum (7 days). Longer values are silently clamped instead of
// rejected; the alternative would be a 400 on the UI default ramp-up while
// users learn the limit.
const presignMaxExpires = 7 * 24 * time.Hour

// presignDefaultExpires is the lifetime used when the caller does not pass
// ?expires. 15 minutes covers "copy a link, paste it into Slack, recipient
// clicks" without being so long that a leaked URL stays useful all day.
const presignDefaultExpires = 15 * time.Minute

// PresignObjectHandler generates a time-limited SigV4 GetObject URL for the
// requested object. The admin user's access key/secret (already authenticated
// upstream by AdminAuthMiddleware) is used to sign — there is no separate
// "presign principal", so a presigned URL grants exactly the same read that
// its issuer was authorised for at sign time.
func PresignObjectHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")
	if bucket == "" || key == "" {
		respondError(c, http.StatusBadRequest, "InvalidRequest", "Bucket and key required")
		return
	}

	expires := presignDefaultExpires
	if raw := c.Query("expires"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			expires = time.Duration(n) * time.Second
			if expires > presignMaxExpires {
				expires = presignMaxExpires
			}
		}
	}

	// The signing identity is the admin user already on the context. We
	// re-decrypt the stored secret here rather than threading it through
	// the auth chain — the cost is a single AES open per presign call.
	v, ok := c.Get("user")
	if !ok {
		respondError(c, http.StatusInternalServerError, "InternalError", "Missing authenticated user")
		return
	}
	u, ok := v.(*storage.User)
	if !ok {
		respondError(c, http.StatusInternalServerError, "InternalError", "Invalid user context")
		return
	}
	secret, err := storage.Decrypt(u.EncryptedSecret)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error decrypting credentials")
		return
	}

	// base is normally populated — it defaults to the localhost storage origin
	// when PUBLIC_BASE_URL is unset. This guards the degenerate case where an
	// operator explicitly configured it empty.
	base := presignBaseURL(c)
	if base == "" {
		respondError(c, http.StatusServiceUnavailable, "PresignUnavailable",
			"Presigned URLs unavailable: server has no public base URL configured")
		return
	}

	url, err := auth.PresignGetURL(u.AccessKeyID, secret, base, bucket, key, expires)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to sign URL: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":       url,
		"expiresIn": int(expires.Seconds()),
		"expiresAt": time.Now().UTC().Add(expires).Format(time.RFC3339),
	})
}

// presignBaseURL resolves the externally-reachable storage origin. The
// PUBLIC_BASE_URL config takes precedence; if unset the call fails rather
// than silently signing against an unreachable host:port. The atomic.Value
// here is the same one populated at startup in handlers/config.go.
func presignBaseURL(_ *gin.Context) string {
	v, _ := publicBaseURL.Load().(string)
	return strings.TrimRight(v, "/")
}

