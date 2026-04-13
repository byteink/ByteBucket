package router

import (
	"ByteBucket/internal/handlers"

	"github.com/gin-gonic/gin"
)

// RegisterStorageRoutes binds the full S3-compatible storage surface onto the
// given router group. It is mounted twice by the process:
//   - at "/"   under the SigV4 middleware on port 9000 (S3 clients)
//   - at "/s3" under the admin middleware on port 9001 (admin UI)
//
// Both mounts share the same handler code; auth middleware publishes the
// user on the Gin context so handlers stay surface-agnostic.
func RegisterStorageRoutes(g gin.IRouter) {
	// List Buckets. Registered at "/" so it works uniformly whether the
	// caller hits "/" on the SigV4 router or "/s3/" on the admin router.
	// Gin's RedirectTrailingSlash handles the no-slash admin form.
	g.GET("/", handlers.ListBucketsHandler)

	// Bucket-level operations.
	g.PUT("/:bucket", handlers.CreateBucketHandler)
	g.GET("/:bucket", handlers.ListObjectsHandler)
	g.DELETE("/:bucket", handlers.DeleteBucketHandler)
	g.HEAD("/:bucket", handlers.HeadBucketHandler)

	// Object-level operations. Because Gin's routing does not split on "/" for
	// wildcard paths, an empty object key (trailing slash on /:bucket/) has
	// historically been treated as a bucket-level operation; keep that.
	g.PUT("/:bucket/*objectKey", func(c *gin.Context) {
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			handlers.CreateBucketHandler(c)
			return
		}
		handlers.UploadObjectHandler(c)
	})
	g.GET("/:bucket/*objectKey", handlers.DownloadObjectHandler)
	g.DELETE("/:bucket/*objectKey", handlers.DeleteObjectHandler)
	g.HEAD("/:bucket/*objectKey", func(c *gin.Context) {
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			handlers.HeadBucketHandler(c)
			return
		}
		handlers.GetObjectMetadataHandler(c)
	})
}
