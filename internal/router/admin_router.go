package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/webui"

	"github.com/gin-gonic/gin"
)

// rateLimitConfigPath is the admin API subpath for the runtime rate-limit
// override, registered for GET/PUT/DELETE below.
const rateLimitConfigPath = "/config/ratelimit"

// syncWritesConfigPath is the admin API subpath for the object-write durability
// (fsync) toggle, registered for GET/PUT below.
const syncWritesConfigPath = "/config/sync"

// retentionConfigPath is the admin API subpath for the request-sample retention
// (in days), registered for GET/PUT below.
const retentionConfigPath = "/config/retention"

// NewAdminRouter initializes the routes for admin operations.
//
// The embedded admin SPA is served at / (and any unknown path) without auth;
// every authenticated admin API endpoint lives under /api/* so SPA routes
// like /users or /buckets cannot collide with server-side handlers. The UI is
// public by design: credentials are collected client-side at login and sent
// on every API call as X-Admin-* headers. The entire admin port is expected
// to be bound to localhost or a private network — see SECURITY.md.
func NewAdminRouter(rlCtrl *middleware.RateLimitController) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// Per-request ID runs before auth so 401/403 responses from the admin
	// middleware carry a correlatable identifier, matching the SigV4 surface.
	r.Use(middleware.RequestIDMiddleware())

	// Structured request log runs after RequestID (so the ID is in every
	// line) and before auth (so 401/403 responses still emit a line). The
	// handler writes at the end of the chain, so placement only affects
	// what the logger can see in the Gin context.
	r.Use(middleware.Log())
	r.Use(middleware.Metrics())

	// Rate limiting runs after Log/Metrics and before auth, matching the
	// storage surface. Per-IP buckets isolate the loopback health probe from a
	// flood originating elsewhere, so the orchestrator check survives an attack
	// on other endpoints. The same controller backs both surfaces, so a client
	// is throttled against one shared budget. Always mounted; short-circuits
	// when disabled so it can be enabled at runtime.
	r.Use(rlCtrl.Middleware())

	// Public, unauthenticated operational endpoints. They stay at the root
	// level so existing probes, dashboards and scrapers keep working.
	r.GET("/health", handlers.HealthHandler)

	// Prometheus scrape endpoint. Deliberately unauthenticated: standard
	// Prometheus practice is to expose /metrics on a private network and
	// rely on network boundaries rather than in-process auth. The admin
	// port is already documented as non-public in SECURITY.md, so mounting
	// here is consistent — do NOT ever expose :9001 on the public
	// internet.
	r.GET("/metrics", gin.WrapH(middleware.PrometheusHandler()))

	// Authenticated admin API. Namespaced under /api so the React SPA's
	// client-side routes (/login, /users, /buckets, ...) cannot shadow a
	// server route on a browser refresh.
	api := r.Group("/api")
	api.Use(auth.AdminAuthMiddleware)
	// Validate any bucket/object identifier before handlers run. The check
	// is idempotent with the per-route validator inside RegisterStorageRoutes;
	// applied here so /api/s3 plus any future /api/* route that adds bucket
	// params inherits the same hardening without remembering to opt in.
	api.Use(middleware.ValidateNames())
	{
		api.GET("/config", handlers.GetConfigHandler)
		api.GET("/stats", handlers.GetStatsHandler)
		api.GET("/stats/requests", handlers.GetRequestSeriesHandler)
		api.GET(retentionConfigPath, handlers.GetRetentionHandler)
		api.PUT(retentionConfigPath, handlers.PutRetentionHandler)
		api.GET(rateLimitConfigPath, handlers.GetRateLimitHandler)
		api.PUT(rateLimitConfigPath, handlers.PutRateLimitHandler)
		api.DELETE(rateLimitConfigPath, handlers.DeleteRateLimitHandler)
		api.GET(syncWritesConfigPath, handlers.GetSyncWritesHandler)
		api.PUT(syncWritesConfigPath, handlers.PutSyncWritesHandler)
		api.POST("/users", handlers.CreateUserHandler)
		api.GET("/users", handlers.ListUsersHandler)
		api.PUT("/users/:accessKeyID", handlers.UpdateUserHandler)
		api.DELETE("/users/:accessKeyID", handlers.DeleteUserHandler)

		// Storage operations mounted under /api/s3 using the same handler
		// code as the SigV4 surface on port 9000. This eliminates a parallel
		// admin implementation of bucket/object CRUD; the admin middleware
		// publishes the authenticated user on the context so the shared
		// handlers need no knowledge of which surface they are serving.
		//
		// Request-health is recorded only on this group so object operations
		// done through the UI count, but admin-management endpoints and SPA
		// asset fetches never pollute the 2xx/4xx/5xx view.
		s3 := api.Group("/s3")
		s3.Use(middleware.S3RequestOutcome())
		RegisterStorageRoutes(s3)
	}

	// Embedded admin SPA. Any path not matched above falls through to the
	// SPA handler which serves static assets or index.html for SPA routes.
	r.NoRoute(gin.WrapH(webui.Handler()))

	return r
}
