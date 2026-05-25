package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// NewStorageRouter sets up Gin routes and middleware in an S3-compatible
// manner. The route table is shared with the admin router via
// RegisterStorageRoutes; this function only wires the SigV4-specific
// middleware and public endpoints. rlCfg carries opt-in rate-limit settings;
// when disabled the limiter middleware is not installed at all.
func NewStorageRouter(rlCfg middleware.RateLimitConfig) *gin.Engine {
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

	// Rate limiting (opt-in) runs after Log/Metrics so a throttled request is
	// still observed and ID-tagged, but BEFORE auth and CORS so an
	// unauthenticated flood is rejected before reaching signature
	// verification and filesystem ACL lookups — the expensive, attacker-
	// reachable surface this protects. Installed only when enabled.
	if rlCfg.Enabled {
		r.Use(middleware.RateLimit(rlCfg))
	}

	// Public health check (no authentication required).
	r.GET("/health", handlers.HealthHandler)

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
