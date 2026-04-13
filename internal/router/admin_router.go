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
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Per-request ID runs before auth so 401/403 responses from the admin
	// middleware carry a correlatable identifier, matching the SigV4 surface.
	r.Use(middleware.RequestIDMiddleware())

	// Public health check.
	r.GET("/health", handlers.HealthHandler)

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
