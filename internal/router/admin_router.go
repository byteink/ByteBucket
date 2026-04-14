package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/webui"

	"github.com/gin-gonic/gin"
)

// NewAdminRouter initializes the routes for admin operations.
//
// The embedded admin SPA is served at / (and any unknown path) without auth;
// admin API endpoints live under an authenticated group. The UI is public by
// design: credentials are collected client-side at login and sent on every
// API call as X-Admin-* headers. The entire admin port is expected to be
// bound to localhost or a private network — see SECURITY.md.
func NewAdminRouter() *gin.Engine {
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

	// Public health check.
	r.GET("/health", handlers.HealthHandler)

	// Prometheus scrape endpoint. Deliberately unauthenticated: standard
	// Prometheus practice is to expose /metrics on a private network and
	// rely on network boundaries rather than in-process auth. The admin
	// port is already documented as non-public in SECURITY.md, so mounting
	// here is consistent — do NOT ever expose :9001 on the public
	// internet.
	r.GET("/metrics", gin.WrapH(middleware.PrometheusHandler()))

	// Authenticated admin API.
	protected := r.Group("/")
	protected.Use(auth.AdminAuthMiddleware)
	{
		protected.POST("/users", handlers.CreateUserHandler)
		protected.GET("/users", handlers.ListUsersHandler)
		protected.PUT("/users/:accessKeyID", handlers.UpdateUserHandler)
		protected.DELETE("/users/:accessKeyID", handlers.DeleteUserHandler)

		// Storage operations mounted under /s3 using the same handler code
		// as the SigV4 surface on port 9000. This eliminates a parallel
		// admin implementation of bucket/object CRUD; the admin middleware
		// publishes the authenticated user on the context so the shared
		// handlers need no knowledge of which surface they are serving.
		s3 := protected.Group("/s3")
		RegisterStorageRoutes(s3)
	}

	// Embedded admin SPA. Any path not matched above falls through to the
	// SPA handler which serves static assets or index.html for SPA routes.
	r.NoRoute(gin.WrapH(webui.Handler()))

	return r
}
