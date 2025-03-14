package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"
	"github.com/gin-gonic/gin"
)

// NewRouter sets up Gin routes and middleware in a S3 API compatible manner.
// This refactoring aligns the endpoints with the S3 REST API conventions.
func NewRouter() *gin.Engine {
	// Create a new Gin engine without the default logger to allow custom middleware
	r := gin.New()

	// Attach recovery middleware to handle panics gracefully
	r.Use(gin.Recovery())

	// Public health check endpoint remains available without authentication
	r.GET("/health", handlers.HealthHandler)

	// All S3 operations require authentication (using S3 signature-based auth middleware)
	r.Use(auth.AuthMiddleware)

	// S3 API: List Buckets
	// GET / returns a list of all buckets
	r.GET("/", handlers.ListBucketsHandler)

	// Bucket-level operations
	// Create Bucket: PUT /:bucket
	r.PUT("/:bucket", handlers.CreateBucketHandler)

	// Delete Bucket: DELETE /:bucket
	r.DELETE("/:bucket", handlers.DeleteBucketHandler)

	// Object-level operations
	// List Objects in a bucket: GET /:bucket (query parameters like ?list-type=2 handled in the handler)
	r.GET("/:bucket", handlers.ListObjectsHandler)

	// Upload Object: PUT /:bucket/*objectKey
	r.PUT("/:bucket/*objectKey", handlers.UploadObjectHandler)

	// Download Object: GET /:bucket/*objectKey
	r.GET("/:bucket/*objectKey", handlers.DownloadObjectHandler)

	// Delete Object: DELETE /:bucket/*objectKey
	r.DELETE("/:bucket/*objectKey", handlers.DeleteObjectHandler)

	return r
}
