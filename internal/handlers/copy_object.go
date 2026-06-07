package handlers

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// copyObjectResult is the <CopyObjectResult> reply document. AWS SDKs read the
// ETag to verify the copy; LastModified is informational.
type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult" json:"-"`
	ETag         string   `xml:"ETag" json:"etag"`
	LastModified string   `xml:"LastModified" json:"lastModified"`
}

// CopyObjectHandler handles PUT /:bucket/:key with an x-amz-copy-source header.
// The source is attacker-controlled and is turned into a filesystem path, so
// both its bucket and key are validated before any IO; a traversal or
// sidecar-named source is rejected with 400 and never read. The metadata
// directive (COPY, default, or REPLACE) decides whether the destination
// inherits the source's metadata or takes it from the request headers.
func CopyObjectHandler(c *gin.Context) {
	dstBucket := c.Param("bucket")
	dstKey := strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")

	srcBucket, srcKey, ok := parseCopySource(c)
	if !ok {
		return
	}

	srcPath := filepath.Join(objectsRoot, srcBucket, srcKey)
	if info, err := os.Stat(srcPath); err != nil || info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Source object not found")
		return
	}

	directive := strings.ToUpper(strings.TrimSpace(c.GetHeader("x-amz-metadata-directive")))
	if directive == "" {
		directive = "COPY"
	}
	if directive != "COPY" && directive != "REPLACE" {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid x-amz-metadata-directive")
		return
	}

	// Copying an object onto itself with no metadata change is a no-op AWS
	// rejects outright; only a REPLACE (which mutates metadata) is legal.
	if srcBucket == dstBucket && srcKey == dstKey && directive == "COPY" {
		respondError(c, http.StatusBadRequest, "InvalidRequest",
			"Copy to self requires the REPLACE metadata directive")
		return
	}

	cannedACL, ok := resolveCannedACL(c)
	if !ok {
		return
	}

	meta, ok := buildCopyMetadata(c, srcPath, directive)
	if !ok {
		return
	}
	if cannedACL != "" {
		meta["acl"] = cannedACL
	}

	src, err := os.Open(srcPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error reading source object")
		return
	}
	defer func() { _ = src.Close() }()

	// finalizeObjectWrite writes to a temp file and renames, so a self-copy
	// (dstPath == srcPath) reads the original bytes via the open handle above
	// while the new content lands in a separate inode before the rename.
	dstPath := filepath.Join(objectsRoot, dstBucket, dstKey)
	etag, written, err := finalizeObjectWrite(dstBucket, dstPath, src, meta)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error copying object")
		return
	}
	middleware.RecordObjectUpload(dstBucket, written)

	if cannedACL != "" {
		auditACLChange(c, "object", dstBucket, dstKey, storage.ACLPrivate, cannedACL)
	}

	c.Header("ETag", etag)
	result := copyObjectResult{ETag: etag, LastModified: time.Now().UTC().Format(time.RFC3339)}
	respondXMLOrJSON(c, http.StatusOK, result, result)
}

// parseCopySource decodes the x-amz-copy-source header into a validated source
// bucket and key. The header is "/bucket/key" (leading slash optional) with the
// key percent-encoded, matching the AWS wire shape. Validation runs through the
// same ValidateBucketName / ValidateObjectKey gates the URL surface uses, so a
// traversal or sidecar-named source cannot reach the filesystem.
func parseCopySource(c *gin.Context) (string, string, bool) {
	raw := strings.TrimPrefix(c.GetHeader("x-amz-copy-source"), "/")
	// A versionId suffix (?versionId=) has no meaning here (no versioning); drop it.
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	bucket, key, found := strings.Cut(raw, "/")
	if !found || bucket == "" || key == "" {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Malformed x-amz-copy-source")
		return "", "", false
	}
	decodedKey, err := url.PathUnescape(key)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Malformed x-amz-copy-source")
		return "", "", false
	}
	if err := storage.ValidateBucketName(bucket); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid copy-source bucket")
		return "", "", false
	}
	cleanKey, err := storage.ValidateObjectKey(decodedKey)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid copy-source key")
		return "", "", false
	}
	return bucket, cleanKey, true
}

// buildCopyMetadata assembles the destination metadata map. REPLACE takes the
// user metadata and Content-Type from the request headers; COPY carries the
// source object's stored Content-Type and x-amz-meta-* entries. The
// checksum/etag/length are stamped later by finalizeObjectWrite, so they are
// deliberately omitted here.
func buildCopyMetadata(c *gin.Context, srcPath, directive string) (map[string]string, bool) {
	if directive == "REPLACE" {
		meta, err := collectUserMetadataChecked(c)
		if err != nil {
			respondError(c, http.StatusBadRequest, "MetadataTooLarge", "x-amz-meta-* headers exceed 2 KiB total")
			return nil, false
		}
		meta[contentTypeMetaKey] = contentTypeOrOctet(c)
		return meta, true
	}

	meta := map[string]string{contentTypeMetaKey: "application/octet-stream"}
	data, err := os.ReadFile(srcPath + ".meta")
	if err != nil {
		// A source without a sidecar still copies; it just carries defaults.
		return meta, true
	}
	var srcMeta map[string]string
	if err := json.Unmarshal(data, &srcMeta); err != nil {
		return meta, true
	}
	if ct := srcMeta[contentTypeMetaKey]; ct != "" {
		meta[contentTypeMetaKey] = ct
	}
	// Match case-insensitively: uploads persist user metadata under Go's
	// canonical header casing (X-Amz-Meta-*), not the lowercase wire form.
	for k, v := range srcMeta {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			meta[k] = v
		}
	}
	return meta, true
}
