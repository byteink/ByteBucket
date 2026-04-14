package router

import (
	"net/http"

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

	// Bucket-level operations. PUT/GET/DELETE /:bucket dispatches to the
	// per-bucket CORS subresource handlers when "?cors" is present; this
	// preserves the S3 wire shape where subresources live on the query
	// string rather than as distinct path segments.
	g.PUT("/:bucket", dispatchBucketSubresource(handlers.CreateBucketHandler, http.MethodPut))
	g.GET("/:bucket", dispatchBucketSubresource(handlers.ListObjectsHandler, http.MethodGet))
	g.DELETE("/:bucket", dispatchBucketSubresource(handlers.DeleteBucketHandler, http.MethodDelete))
	g.HEAD("/:bucket", handlers.HeadBucketHandler)

	// Object-level operations. Because Gin's routing does not split on "/"
	// for wildcard paths, an empty object key (trailing slash on /:bucket/)
	// has historically been treated as a bucket-level operation; keep that.
	g.PUT("/:bucket/*objectKey", dispatchObjectPUT)
	g.GET("/:bucket/*objectKey", dispatchObjectGET)
	g.DELETE("/:bucket/*objectKey", dispatchObjectDELETE)
	g.POST("/:bucket/*objectKey", dispatchObjectPOST)
	g.HEAD("/:bucket/*objectKey", func(c *gin.Context) {
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			handlers.HeadBucketHandler(c)
			return
		}
		handlers.GetObjectMetadataHandler(c)
	})
}

// dispatchObjectPUT routes object-level PUTs between single-PUT uploads and
// UploadPart (multipart). The "uploadId" + "partNumber" query params are the
// S3-defined disambiguators; presence of both flips us onto the multipart
// path. An empty object key falls through to CreateBucket, matching the
// historical behaviour of trailing-slash bucket addressing.
func dispatchObjectPUT(c *gin.Context) {
	objectKey := c.Param("objectKey")
	if objectKey == "" || objectKey == "/" {
		handlers.CreateBucketHandler(c)
		return
	}
	q := c.Request.URL.Query()
	if q.Get("uploadId") != "" && q.Get("partNumber") != "" {
		handlers.UploadPartHandler(c)
		return
	}
	handlers.UploadObjectHandler(c)
}

// dispatchObjectGET routes GET between plain downloads and ListParts.
func dispatchObjectGET(c *gin.Context) {
	if c.Request.URL.Query().Get("uploadId") != "" {
		handlers.ListPartsHandler(c)
		return
	}
	handlers.DownloadObjectHandler(c)
}

// dispatchObjectDELETE routes DELETE between plain object delete and
// AbortMultipartUpload.
func dispatchObjectDELETE(c *gin.Context) {
	if c.Request.URL.Query().Get("uploadId") != "" {
		handlers.AbortMultipartUploadHandler(c)
		return
	}
	handlers.DeleteObjectHandler(c)
}

// dispatchObjectPOST is the multipart-only POST dispatcher. S3 reserves POST
// on an object path for multipart initiate (?uploads) and complete
// (?uploadId). Anything else is an unsupported POST and returns 405.
func dispatchObjectPOST(c *gin.Context) {
	q := c.Request.URL.Query()
	if _, ok := q["uploads"]; ok {
		handlers.CreateMultipartUploadHandler(c)
		return
	}
	if q.Get("uploadId") != "" {
		handlers.CompleteMultipartUploadHandler(c)
		return
	}
	c.Status(http.StatusMethodNotAllowed)
}

// dispatchBucketSubresource picks between the default bucket handler and a
// subresource handler based on query parameters. Today ?cors and ?uploads
// are recognised; ?acl, ?policy, ?lifecycle, etc. fall through to the
// default handler. Adding a new subresource means one more case here,
// nothing else.
func dispatchBucketSubresource(defaultHandler gin.HandlerFunc, method string) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Request.URL.Query()
		if _, ok := q["cors"]; ok {
			switch method {
			case http.MethodPut:
				handlers.PutBucketCORSHandler(c)
			case http.MethodGet:
				handlers.GetBucketCORSHandler(c)
			case http.MethodDelete:
				handlers.DeleteBucketCORSHandler(c)
			}
			return
		}
		if _, ok := q["uploads"]; ok && method == http.MethodGet {
			handlers.ListMultipartUploadsHandler(c)
			return
		}
		defaultHandler(c)
	}
}
