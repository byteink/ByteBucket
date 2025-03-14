package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"

	"github.com/gin-gonic/gin"
)

// NewRouter sets up Gin routes and middleware.
func NewRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/", handlers.HomeHandler)

	// Public health check endpoint.
	r.GET("/health", handlers.HealthHandler)

	// Authenticated routes.
	authGroup := r.Group("/")
	authGroup.Use(auth.AuthMiddleware)
	{
		// Bucket endpoints.
		buckets := authGroup.Group("/buckets")
		{
			buckets.POST("/", handlers.CreateBucketHandler)
			buckets.GET("/", handlers.ListBucketsHandler)
			buckets.DELETE("/:bucketName", handlers.DeleteBucketHandler)

			// Object endpoints.
			objects := buckets.Group("/:bucketName/objects")
			{
				objects.POST("/", handlers.UploadObjectHandler)
				objects.GET("/", handlers.ListObjectsHandler)
				// Use a wildcard to support nested paths in object keys.
				objects.GET("/*objectKey", handlers.DownloadObjectHandler)
				objects.DELETE("/*objectKey", handlers.DeleteObjectHandler)
			}
		}

		// Presigned URL endpoints.
		presign := authGroup.Group("/presign")
		{
			presign.GET("/upload", handlers.PresignUploadHandler)
			presign.GET("/download", handlers.PresignDownloadHandler)
		}

		// User management endpoints.
		users := authGroup.Group("/users")
		{
			// POST /users auto-generates keys.
			users.POST("/", handlers.CreateUserHandler)
			users.GET("/", handlers.ListUsersHandler)
		}
	}

	// Public object access in S3‑style:
	// GET /:bucket/*objectKey bypasses authentication.
	r.GET("/:bucket/*objectKey", handlers.PublicDownloadObjectHandler)

	return r
}
