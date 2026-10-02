package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

const (
	// sessionCookiePath scopes the cookie to the admin API so SPA asset and
	// page requests never carry it.
	sessionCookiePath = "/api"
	// loginBodyLimit bounds the login JSON far above any real access key and
	// secret pair while keeping a hostile body from being buffered.
	loginBodyLimit = 4 << 10
	authMethodHdr  = "admin"
	authMethodSess = "admin-session"
	msgInvalidCred = "Invalid admin credentials"
)

var (
	errInvalidCredentials = errors.New("invalid admin credentials")
	errNotAdmin           = errors.New("not an admin")
	errDecrypt            = errors.New("decrypt failed")
)

// dummyCiphertext has the shape of a stored 40-character secret (nonce +
// ciphertext + GCM tag). It is decrypted on the unknown-user path so a miss
// does the same Bolt lookup, AES-GCM open and digest compare as a wrong
// secret, keeping response timing from revealing which access keys exist.
// It is random filler, never a real secret, and always fails authentication.
const dummyCiphertext = "ch64EUHWc/HmXcBYRxjzoqAfNUT580P6SB9frZK8sxekPlCj37I08MsUY5RCLX7j4xSCJM0qGpMkNzi3/SBsc7taFio="

// secretsEqual compares two secrets in time independent of where they differ
// and of their lengths: both sides are reduced to fixed-size digests first.
func secretsEqual(a, b string) bool {
	da := sha256.Sum256([]byte(a))
	db := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}

// isAdmin reports whether the user's ACL carries the Allow */* rule that
// defines an admin.
func isAdmin(u *storage.User) bool {
	for _, rule := range u.ACL {
		if strings.EqualFold(rule.Effect, "Allow") && containsWildcard(rule.Buckets) && containsWildcard(rule.Actions) {
			return true
		}
	}
	return false
}

func containsWildcard(items []string) bool {
	for _, it := range items {
		if it == "*" {
			return true
		}
	}
	return false
}

// verifyAdmin checks an access key and secret pair and requires admin rights.
// Unknown key and wrong secret return the same error after the same work.
func verifyAdmin(accessKey, secret string) (*storage.User, error) {
	user, lookupErr := storage.GetUser(accessKey)
	if lookupErr != nil {
		// Both results are discarded on purpose: this branch exists only to
		// spend the same work as the found-user branch below.
		_, _ = storage.Decrypt(dummyCiphertext)
		_ = secretsEqual(secret, dummyCiphertext)
		return nil, errInvalidCredentials
	}
	stored, err := storage.Decrypt(user.EncryptedSecret)
	if err != nil {
		return nil, errDecrypt
	}
	if !secretsEqual(secret, stored) {
		return nil, errInvalidCredentials
	}
	if !isAdmin(user) {
		return nil, errNotAdmin
	}
	return user, nil
}

type originVerdict int

const (
	originUnknown originVerdict = iota // no browser evidence either way
	originSame
	originCross
)

// checkOrigin classifies a request by Fetch Metadata, falling back to the
// Origin header for browsers that predate Sec-Fetch-Site. Only same-origin
// counts as same: same-site siblings and typed-URL navigations ("none") are
// not the admin UI. The Origin host is compared with the request Host rather
// than a configured URL, so it holds behind a TLS-terminating proxy too.
func checkOrigin(r *http.Request) originVerdict {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site == "same-origin" {
			return originSame
		}
		return originCross
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return originUnknown
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return originCross
	}
	return originSame
}

// requestIsHTTPS reports whether the browser reached us over TLS, directly or
// through a TLS-terminating proxy. Trusting X-Forwarded-Proto here is safe:
// a spoofed "https" only adds Secure to the spoofer's own cookie, which a
// plain-http browser then refuses, and local plain-http dev stays usable.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// setSessionCookie writes the session cookie. It deliberately has no
// Max-Age/Expires: a browser-session cookie dies with the browser, and the
// server enforces idle and absolute expiry regardless.
func setSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     sessionCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(c.Request),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(c *gin.Context) {
	setSessionCookie(c, "", -1)
}

func abortJSON(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// failCredentials maps a verifyAdmin error to the response and counts a
// wrong secret toward the caller's lockout.
func failCredentials(c *gin.Context, ip string, err error) {
	switch {
	case errors.Is(err, errInvalidCredentials):
		failures.fail(ip)
		slog.Warn("admin credential failure", "client_ip", ip)
		abortJSON(c, http.StatusUnauthorized, msgInvalidCred)
	case errors.Is(err, errNotAdmin):
		abortJSON(c, http.StatusUnauthorized, "User does not have admin privileges")
	default:
		abortJSON(c, http.StatusInternalServerError, "Error decrypting secret")
	}
}

// checkCredentials runs the lockout gate and credential verification shared
// by header auth and the login form. On failure it has already responded.
func checkCredentials(c *gin.Context, accessKey, secret string) (*storage.User, bool) {
	ip := middleware.ResolveClientIP(c.Request)
	if retry, locked := failures.blocked(ip); locked {
		c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())))
		abortJSON(c, http.StatusTooManyRequests, "Too many failed admin login attempts")
		return nil, false
	}
	user, err := verifyAdmin(accessKey, secret)
	if err != nil {
		failCredentials(c, ip, err)
		return nil, false
	}
	failures.reset(ip)
	return user, true
}

func publishAdmin(c *gin.Context, user *storage.User, method string) {
	c.Set("user", user)
	c.Set("authMethod", method)
	c.Next()
}

// cookieAuth authenticates a browser request by its session cookie. The
// same-origin proof is mandatory here (defence in depth over SameSite=Strict):
// a cookie is ambient, so its presence alone proves nothing about intent.
func cookieAuth(c *gin.Context, token string) {
	if checkOrigin(c.Request) != originSame {
		abortJSON(c, http.StatusForbidden, "Cross-origin request rejected")
		return
	}
	accessKey, ok := sessions.lookup(token)
	if !ok {
		clearSessionCookie(c)
		abortJSON(c, http.StatusUnauthorized, "Session expired")
		return
	}
	// Re-read the user every request so deleting or demoting an admin ends
	// their live sessions immediately.
	user, err := storage.GetUser(accessKey)
	if err != nil || !isAdmin(user) {
		sessions.revoke(token)
		clearSessionCookie(c)
		abortJSON(c, http.StatusUnauthorized, "Session revoked")
		return
	}
	publishAdmin(c, user, authMethodSess)
}

type loginBody struct {
	AccessKey string `json:"accessKey"`
	Secret    string `json:"secret"`
}

func readLoginBody(c *gin.Context) (loginBody, bool) {
	var body loginBody
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, loginBodyLimit+1))
	if err != nil || len(raw) > loginBodyLimit || json.Unmarshal(raw, &body) != nil {
		return body, false
	}
	return body, body.AccessKey != "" && body.Secret != ""
}

// LoginHandler exchanges an admin access key and secret for a session cookie.
// The secret crosses the wire once; the browser never stores it.
func LoginHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if checkOrigin(c.Request) == originCross {
		abortJSON(c, http.StatusForbidden, "Cross-origin request rejected")
		return
	}
	body, ok := readLoginBody(c)
	if !ok {
		abortJSON(c, http.StatusBadRequest, "Malformed login request")
		return
	}
	user, ok := checkCredentials(c, body.AccessKey, body.Secret)
	if !ok {
		return
	}
	// Never carry a pre-login token across authentication (session fixation).
	if old, err := c.Cookie(SessionCookieName); err == nil {
		sessions.revoke(old)
	}
	setSessionCookie(c, sessions.create(user.AccessKeyID), 0)
	c.JSON(http.StatusOK, gin.H{"accessKey": user.AccessKeyID})
}

// LogoutHandler revokes the presented session server-side and clears the
// cookie. It needs no valid session, so it is idempotent.
func LogoutHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if checkOrigin(c.Request) == originCross {
		abortJSON(c, http.StatusForbidden, "Cross-origin request rejected")
		return
	}
	if token, err := c.Cookie(SessionCookieName); err == nil {
		sessions.revoke(token)
	}
	clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

// SessionHandler reports who the current admin request is authenticated as,
// so the UI can tell whether its cookie is still live.
func SessionHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user := c.MustGet("user").(*storage.User)
	c.JSON(http.StatusOK, gin.H{"accessKey": user.AccessKeyID})
}
