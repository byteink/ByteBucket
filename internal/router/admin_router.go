package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"

	"github.com/gin-gonic/gin"
)

// NewAdminRouter initializes the routes for admin operations.
func NewAdminRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Public health check.
	r.GET("/health", handlers.HealthHandler)

	// Protect admin endpoints with admin auth middleware.
	r.Use(auth.AdminAuthMiddleware)

	// User management routes.
	r.POST("/users", handlers.CreateUserHandler)
	r.GET("/users", handlers.ListUsersHandler)
	r.PUT("/users/:id", handlers.UpdateUserHandler)
	r.DELETE("/users/:id", handlers.DeleteUserHandler)

	// CORS configuration endpoints
	r.GET("/cors", handlers.GetCORSConfigHandler)
	r.PUT("/cors", handlers.UpdateCORSConfigHandler)

	// Optionally, add additional system configuration endpoints.
	// r.GET("/config", handlers.GetConfigHandler)
	// r.PUT("/config", handlers.UpdateConfigHandler)

	return r
}
