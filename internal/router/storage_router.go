package router

import (
	"net/http"

	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/webui"

	"github.com/gin-gonic/gin"
)

// faviconHandler serves the embedded favicon on the storage surface. A browser
// opening a public-object link on the storage origin probes /favicon.ico; without
// this it matches the /:bucket route and 400s on the invalid bucket name. Public
// and static — no auth, no user input — so it is registered alongside /health,
// before the auth/validation middleware. 404 when the UI bundle is unbuilt.
func faviconHandler(c *gin.Context) {
	icon, ok := webui.FaviconICO()
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "image/vnd.microsoft.icon", icon)
}

// NewStorageRouter sets up Gin routes and middleware in an S3-compatible
// manner. The route table is shared with the admin router via
// RegisterStorageRoutes; this function only wires the SigV4-specific
// middleware and public endpoints. rlCtrl carries the live rate-limit
// controller shared with the admin router; its middleware is always mounted
// and short-circuits when limiting is disabled.
func NewStorageRouter(rlCtrl *middleware.RateLimitController) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// Per-request ID runs first so every response — including errors from
	// downstream middleware (CORS, auth) — carries a correlatable identifier.
	r.Use(middleware.RequestIDMiddleware())

	// Structured request log sits immediately after RequestID and before the
	// CORS/auth middlewares so every response — including preflight denials
	// and SigV4 rejections — produces one JSON log line.
	r.Use(middleware.Log())

	// Metrics sits alongside Log so both surfaces observe the same request
	// envelope. Label cardinality is bounded because path is c.FullPath().
	r.Use(middleware.Metrics())

	// Per-class request-health for the S3 data-plane. The whole :9000 surface
	// is real object traffic, so it is mounted globally (the middleware itself
	// excludes /health), unlike the admin surface where only /api/s3 qualifies.
	r.Use(middleware.S3RequestOutcome())

	// Data-plane access log. Same data-plane scope as S3RequestOutcome; a no-op
	// (one atomic load) when access logging is disabled.
	r.Use(middleware.AccessLog())

	// Rate limiting runs after Log/Metrics so a throttled request is still
	// observed and ID-tagged, but BEFORE auth and CORS so an unauthenticated
	// flood is rejected before reaching signature verification and filesystem
	// ACL lookups — the expensive, attacker-reachable surface this protects.
	// Always mounted; the controller short-circuits when limiting is disabled
	// so it can be enabled at runtime without a restart.
	r.Use(rlCtrl.Middleware())

	// Public health check (no authentication required).
	r.GET("/health", handlers.HealthHandler)

	// Favicon: registered before the auth/validation middleware so a browser
	// probing the storage origin gets the icon (200) instead of a bucket-name
	// 400. No valid bucket can be named "favicon.ico", so this shadows nothing.
	r.GET("/favicon.ico", faviconHandler)

	// Per-bucket CORS must run before SigV4 so browser preflights (which are
	// unauthenticated) can be answered. Buckets without a CORS config return
	// 403 for preflights, matching S3 behaviour; there is no global CORS
	// policy anymore.
	r.Use(middleware.BucketCORSMiddleware())

	// Validate bucket/object names BEFORE auth so the anonymous-read branch
	// of AuthMiddleware never sees an unvalidated identifier — otherwise a
	// crafted URL-encoded ".." segment could escape ObjectsRoot when auth
	// constructs the file path for its ACL lookup.
	r.Use(middleware.ValidateNames())

	// All S3 operations below require SigV4 authentication. AuthMiddleware
	// publishes the authenticated user on the Gin context; the shared storage
	// handlers read it from there.
	r.Use(auth.AuthMiddleware)
	RegisterStorageRoutes(r)

	return r
}
