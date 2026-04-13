package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// NewStorageRouter sets up Gin routes and middleware in an S3-compatible
// manner. The actual route table is shared with the admin router via
// RegisterStorageRoutes — this function only wires the SigV4-specific
// middleware and public endpoints.
func NewStorageRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// CORS must run before auth so OPTIONS preflights bypass SigV4.
	// The global middleware is replaced with per-bucket CORS in a later
	// commit; keeping it here until then preserves preflight behaviour.
	r.Use(middleware.CORSMiddleware())

	// Public health check (no authentication required).
	r.GET("/health", handlers.HealthHandler)

	// Preflight handler. The CORS middleware has already populated the
	// response headers; we just acknowledge with 204.
	r.OPTIONS("/*path", func(c *gin.Context) {
		c.Status(204)
	})

	// All S3 operations below require SigV4 authentication. AuthMiddleware
	// publishes the authenticated user on the Gin context; the shared storage
	// handlers read it from there.
	r.Use(auth.AuthMiddleware)
	RegisterStorageRoutes(r)

	return r
}
