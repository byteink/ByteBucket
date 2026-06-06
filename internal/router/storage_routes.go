package router

import (
	"net/http"

	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// bucketPath is the bucket-level route pattern shared by every verb.
const bucketPath = "/:bucket"

// methodNotAllowed is the default handler for verbs that only exist to carry a
// subresource (e.g. POST /:bucket?delete); a bare request returns 405.
func methodNotAllowed(c *gin.Context) { c.Status(http.StatusMethodNotAllowed) }

// RegisterStorageRoutes binds the full S3-compatible storage surface onto the
// given router group. It is mounted twice by the process:
//   - at "/"       under the SigV4 middleware on port 9000 (S3 clients)
//   - at "/api/s3" under the admin middleware on port 9001 (admin UI)
//
// Both mounts share the same handler code; auth middleware publishes the
// user on the Gin context so handlers stay surface-agnostic.
func RegisterStorageRoutes(g gin.IRouter) {
	// List Buckets. Registered at "/" so it works uniformly whether the
	// caller hits "/" on the SigV4 router or "/s3/" on the admin router.
	// Gin's RedirectTrailingSlash handles the no-slash admin form.
	g.GET("/", handlers.ListBucketsHandler)

	// Every route below carries a :bucket or :bucket/*objectKey parameter
	// that is attacker-controlled. ValidateNames runs before the dispatch
	// to reject malformed names (path traversal, NUL bytes, reserved
	// sidecar paths) so no handler ever sees an unvalidated identifier.
	v := middleware.ValidateNames()

	// Bucket-level operations. PUT/GET/DELETE /:bucket dispatches to the
	// per-bucket CORS subresource handlers when "?cors" is present; this
	// preserves the S3 wire shape where subresources live on the query
	// string rather than as distinct path segments.
	g.PUT(bucketPath, v, dispatchBucketSubresource(handlers.CreateBucketHandler, http.MethodPut))
	g.GET(bucketPath, v, dispatchBucketSubresource(handlers.ListObjectsHandler, http.MethodGet))
	g.DELETE(bucketPath, v, dispatchBucketSubresource(handlers.DeleteBucketHandler, http.MethodDelete))
	// POST /:bucket exists only for the ?delete batch subresource; any other
	// bucket-level POST is unsupported and the default handler returns 405.
	g.POST(bucketPath, v, dispatchBucketSubresource(methodNotAllowed, http.MethodPost))
	g.HEAD(bucketPath, v, handlers.HeadBucketHandler)

	// Object-level operations. Because Gin's routing does not split on "/"
	// for wildcard paths, an empty object key (trailing slash on /:bucket/)
	// has historically been treated as a bucket-level operation; keep that.
	g.PUT("/:bucket/*objectKey", v, dispatchObjectPUT)
	g.GET("/:bucket/*objectKey", v, dispatchObjectGET)
	g.DELETE("/:bucket/*objectKey", v, dispatchObjectDELETE)
	g.POST("/:bucket/*objectKey", v, dispatchObjectPOST)
	g.HEAD("/:bucket/*objectKey", v, func(c *gin.Context) {
		objectKey := c.Param("objectKey")
		if objectKey == "" || objectKey == "/" {
			handlers.HeadBucketHandler(c)
			return
		}
		handlers.GetObjectMetadataHandler(c)
	})
}

// dispatchObjectPUT routes object-level PUTs between single-PUT uploads,
// UploadPart (multipart), and the ?acl subresource. The "uploadId" +
// "partNumber" query params are the S3-defined disambiguators; presence of
// both flips us onto the multipart path. An empty object key falls through
// to CreateBucket, matching the historical behaviour of trailing-slash
// bucket addressing.
func dispatchObjectPUT(c *gin.Context) {
	objectKey := c.Param("objectKey")
	if objectKey == "" || objectKey == "/" {
		handlers.CreateBucketHandler(c)
		return
	}
	q := c.Request.URL.Query()
	if _, ok := q["acl"]; ok {
		handlers.PutObjectACLHandler(c)
		return
	}
	if _, ok := q["tagging"]; ok {
		handlers.PutObjectTaggingHandler(c)
		return
	}
	if q.Get("uploadId") != "" && q.Get("partNumber") != "" {
		handlers.UploadPartHandler(c)
		return
	}
	// A PUT carrying x-amz-copy-source is a server-side copy, not a body upload.
	if c.GetHeader("x-amz-copy-source") != "" {
		handlers.CopyObjectHandler(c)
		return
	}
	handlers.UploadObjectHandler(c)
}

// dispatchObjectGET routes GET between plain downloads, ListParts, the
// ?acl subresource, and the ?presign admin-only operation. ?presign is not
// a real S3 verb; we expose it here so the admin UI can generate a
// time-limited download URL without reimplementing SigV4 in the browser.
func dispatchObjectGET(c *gin.Context) {
	q := c.Request.URL.Query()
	if _, ok := q["acl"]; ok {
		handlers.GetObjectACLHandler(c)
		return
	}
	if _, ok := q["tagging"]; ok {
		handlers.GetObjectTaggingHandler(c)
		return
	}
	if _, ok := q["presign"]; ok {
		handlers.PresignObjectHandler(c)
		return
	}
	if q.Get("uploadId") != "" {
		handlers.ListPartsHandler(c)
		return
	}
	handlers.DownloadObjectHandler(c)
}

// dispatchObjectDELETE routes DELETE between plain object delete and
// AbortMultipartUpload.
func dispatchObjectDELETE(c *gin.Context) {
	q := c.Request.URL.Query()
	if _, ok := q["tagging"]; ok {
		handlers.DeleteObjectTaggingHandler(c)
		return
	}
	if q.Get("uploadId") != "" {
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
// dispatchBucketCORS routes a ?cors request to the verb-specific handler.
// Split out so dispatchBucketSubresource stays a flat list of subresource
// guards rather than nesting a method switch inside each one.
func dispatchBucketCORS(c *gin.Context, method string) {
	switch method {
	case http.MethodPut:
		handlers.PutBucketCORSHandler(c)
	case http.MethodGet:
		handlers.GetBucketCORSHandler(c)
	case http.MethodDelete:
		handlers.DeleteBucketCORSHandler(c)
	}
}

func dispatchBucketSubresource(defaultHandler gin.HandlerFunc, method string) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Request.URL.Query()
		if _, ok := q["cors"]; ok {
			dispatchBucketCORS(c, method)
			return
		}
		if _, ok := q["acl"]; ok {
			switch method {
			case http.MethodPut:
				handlers.PutBucketACLHandler(c)
			case http.MethodGet:
				handlers.GetBucketACLHandler(c)
			}
			return
		}
		if _, ok := q["uploads"]; ok && method == http.MethodGet {
			handlers.ListMultipartUploadsHandler(c)
			return
		}
		if _, ok := q["delete"]; ok && method == http.MethodPost {
			handlers.DeleteObjectsHandler(c)
			return
		}
		defaultHandler(c)
	}
}
