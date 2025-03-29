package router

import (
	"ByteBucket/internal/auth"
	"ByteBucket/internal/handlers"

	"fmt"

	"github.com/gin-gonic/gin"
)

// NewStorageRouter sets up Gin routes and middleware in a S3 API compatible manner.
// Bucket creation is performed on a PUT request to "/:bucket", and object uploads on PUT "/:bucket/*objectKey".
// If the object key is empty (for example, the URL ends with a trailing slash), the CreateBucketHandler is used.
func NewStorageRouter() *gin.Engine {
	r := gin.New()

	r.Use(gin.Logger())

	// Attach recovery middleware to handle panics gracefully.
	r.Use(gin.Recovery())

	// Add a debug middleware to log all requests
	r.Use(func(c *gin.Context) {
		fmt.Printf("[DEBUG ROUTER] Request: %s %s\n", c.Request.Method, c.Request.URL.Path)
		fmt.Printf("[DEBUG ROUTER] Bucket param: %s\n", c.Param("bucket"))
		fmt.Printf("[DEBUG ROUTER] ObjectKey param: %s\n", c.Param("objectKey"))
		c.Next()
	})

	// Public health check endpoint (no authentication required).
	r.GET("/health", handlers.HealthHandler)

	// All S3 operations below require authentication.
	r.Use(auth.AuthMiddleware)

	// S3 API: List Buckets – GET / returns a list of all buckets.
	r.GET("/", handlers.ListBucketsHandler)

	// Bucket-level operations.
	// Create Bucket: PUT /:bucket
	r.PUT("/:bucket", handlers.CreateBucketHandler)
	// Delete Bucket: DELETE /:bucket
	r.DELETE("/:bucket", handlers.DeleteBucketHandler)

	// Head Bucket: HEAD /:bucket - check if a bucket exists and the caller has permission
	r.HEAD("/:bucket", func(c *gin.Context) {
		fmt.Printf("[DEBUG ROUTER] HEAD request for bucket: %s\n", c.Param("bucket"))
		handlers.HeadBucketHandler(c)
	})

	// Object-level operations.
	// List Objects in a bucket: GET /:bucket (query parameters like ?list-type=2 handled in the handler)
	r.GET("/:bucket", handlers.ListObjectsHandler)
	// Upload Object: PUT /:bucket/*objectKey
	r.PUT("/:bucket/*objectKey", func(c *gin.Context) {
		// If the wildcard parameter is empty (or just "/"), treat it as a bucket creation request.
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			fmt.Printf("[DEBUG ROUTER] Treating as bucket creation: %s\n", c.Param("bucket"))
			handlers.CreateBucketHandler(c)
			return
		}
		handlers.UploadObjectHandler(c)
	})
	// Download Object: GET /:bucket/*objectKey
	r.GET("/:bucket/*objectKey", handlers.DownloadObjectHandler)

	// Delete Object: DELETE /:bucket/*objectKey
	r.DELETE("/:bucket/*objectKey", handlers.DeleteObjectHandler)

	// HEAD Object: Retrieve object metadata (compatible with S3 SDK HeadObject API)
	r.HEAD("/:bucket/*objectKey", func(c *gin.Context) {
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			fmt.Printf("[DEBUG ROUTER] Treating as bucket head operation: %s\n", c.Param("bucket"))
			handlers.HeadBucketHandler(c)
			return
		}
		fmt.Printf("[DEBUG ROUTER] HEAD request for object: %s/%s\n", c.Param("bucket"), c.Param("objectKey"))
		handlers.GetObjectMetadataHandler(c)
	})

	return r
}
