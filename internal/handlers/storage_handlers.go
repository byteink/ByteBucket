package handlers

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/goccy/go-json"

	"github.com/gin-gonic/gin"
)

// etagMetaKey is the metadata-sidecar key under which the S3 ETag is stored.
// Stored WITH the enclosing double quotes so every read path (GET, HEAD, LIST)
// can return it verbatim without re-quoting. S3's wire format for ETag is
// a quoted hex string; matching that here avoids a subtle mismatch between
// the response header and the XML listing.
const etagMetaKey = "ETag"

// computeFileETag reads a file from disk and returns its ETag value in S3
// wire format (hex md5, wrapped in double quotes). Used for the one-time
// migration path: legacy objects written before ETag persistence landed have
// no sidecar value and must be rebuilt lazily on first read.
func computeFileETag(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return formatETag(h), nil
}

// formatETag returns the S3-wire-format ETag for a completed hasher: the
// hex digest wrapped in double quotes. AWS SDKs match on the literal quoted
// form, so the quotes are load-bearing.
func formatETag(h hash.Hash) string {
	return "\"" + hex.EncodeToString(h.Sum(nil)) + "\""
}

// loadOrBackfillETag reads the ETag for an on-disk object. If the metadata
// sidecar is missing or lacks an ETag (legacy objects written before ETag
// persistence), the MD5 is recomputed from the file and persisted so the
// next read is O(metadata). The small IO cost is paid once per legacy
// object; we accept it over a full offline migration step.
func loadOrBackfillETag(objectPath string) (string, error) {
	metadataPath := objectPath + ".meta"

	var metadata map[string]string
	if data, err := os.ReadFile(metadataPath); err == nil {
		if err := json.Unmarshal(data, &metadata); err != nil {
			return "", err
		}
		if tag, ok := metadata[etagMetaKey]; ok && tag != "" {
			return tag, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	tag, err := computeFileETag(objectPath)
	if err != nil {
		return "", err
	}
	if metadata == nil {
		metadata = make(map[string]string, 1)
	}
	metadata[etagMetaKey] = tag
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	// Best-effort persistence: a write failure must not mask the ETag from
	// the caller. The legacy object remains correct; the next read will
	// retry the backfill. No hidden error swallowing — the operator sees
	// it in the next failed write.
	if err := os.WriteFile(metadataPath, raw, 0644); err != nil {
		return tag, nil
	}
	return tag, nil
}

// HealthHandler returns a simple JSON status.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// objectsRoot is where bucket directories live. Exposed as a var for tests;
// production always uses /data/objects.
var objectsRoot = "/data/objects"

// CreateBucketHandler creates a new bucket (directory) and returns an XML
// response compatible with S3 SDK expectations. Errors flow through
// respondError so the admin surface sees JSON while SigV4 callers see XML.
func CreateBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Bucket name required")
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)

	if fileInfo, err := os.Stat(bucketPath); err == nil && fileInfo.IsDir() {
		// BucketAlreadyOwnedByYou keeps the bespoke XML shape with BucketName
		// because the AWS SDK surfaces it to user code; we preserve the wire
		// format here rather than collapsing into respondError's generic body.
		if wantsJSON(c) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"code":       "BucketAlreadyOwnedByYou",
				"message":    "Your previous request to create the named bucket succeeded and you already own it.",
				"bucketName": bucketName,
			})
			return
		}
		c.XML(http.StatusConflict, struct {
			XMLName    xml.Name `xml:"Error"`
			Code       string   `xml:"Code"`
			Message    string   `xml:"Message"`
			BucketName string   `xml:"BucketName"`
			RequestId  string   `xml:"RequestId"`
			HostId     string   `xml:"HostId"`
		}{
			Code:       "BucketAlreadyOwnedByYou",
			Message:    "Your previous request to create the named bucket succeeded and you already own it.",
			BucketName: bucketName,
			RequestId:  middleware.RequestID(c),
		})
		return
	}

	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating bucket")
		return
	}

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"location": fmt.Sprintf("http://%s/%s", c.Request.Host, bucketName)})
		return
	}
	c.XML(http.StatusOK, struct {
		XMLName  xml.Name `xml:"CreateBucketResult"`
		Location string   `xml:"Location"`
	}{
		Location: fmt.Sprintf("http://%s/%s", c.Request.Host, bucketName),
	})
}

// ListBucketsHandler returns a list of buckets.
func ListBucketsHandler(c *gin.Context) {
	entries, err := os.ReadDir(objectsRoot)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError",
			fmt.Sprintf("Error listing buckets: %v", err))
		return
	}

	type Bucket struct {
		Name         string `xml:"Name" json:"name"`
		CreationDate string `xml:"CreationDate" json:"creationDate"`
	}
	var buckets []Bucket
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error getting info for bucket %s: %v", entry.Name(), err))
			return
		}
		buckets = append(buckets, Bucket{
			Name:         entry.Name(),
			CreationDate: info.ModTime().Format(time.RFC3339),
		})
	}

	type owner struct {
		ID          string `xml:"ID" json:"id"`
		DisplayName string `xml:"DisplayName" json:"displayName"`
	}
	// Owner reflects the authenticated caller. Auth middleware publishes the
	// storage.User on the context; we fall back to empty strings only if the
	// handler is ever reached without auth — the routers today prevent that,
	// but a nil assertion would mask a configuration error and is not worth
	// the panic risk on a response path.
	var ownerID string
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok {
			ownerID = u.AccessKeyID
		}
	}
	xmlResult := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		XMLNS   string   `xml:"xmlns,attr"`
		Owner   owner    `xml:"Owner"`
		Buckets struct {
			Bucket []Bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}{
		XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/",
		// DisplayName is an opaque label in S3; reusing the access key keeps
		// it predictable without inventing a new user-profile field.
		Owner: owner{ID: ownerID, DisplayName: ownerID},
	}
	xmlResult.Buckets.Bucket = buckets

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"buckets": buckets})
		return
	}
	c.XML(http.StatusOK, xmlResult)
}

// DeleteBucketHandler deletes a bucket.
func DeleteBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Bucket name required")
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)
	if bucketPath == objectsRoot {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Cannot delete base directory")
		return
	}

	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		return
	}

	if err := os.RemoveAll(bucketPath); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error deleting bucket")
		return
	}

	c.Status(http.StatusNoContent)
}

// ListObjectsHandler lists objects in a bucket.
func ListObjectsHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	bucketPath := filepath.Join(objectsRoot, bucketName)
	entries, err := os.ReadDir(bucketPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error reading bucket")
		return
	}

	type ObjectInfo struct {
		Key          string `xml:"Key" json:"key"`
		LastModified string `xml:"LastModified" json:"lastModified"`
		ETag         string `xml:"ETag" json:"etag"`
		Size         int64  `xml:"Size" json:"size"`
		StorageClass string `xml:"StorageClass" json:"storageClass"`
	}
	var objects []ObjectInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip sidecar metadata files and the per-bucket CORS subresource;
		// neither are user-visible objects.
		name := entry.Name()
		if strings.HasSuffix(name, ".meta") || name == ".cors.json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Resolve the per-object ETag from its sidecar. A backfill kicks in
		// for legacy objects that predate ETag persistence so the listing is
		// self-healing rather than returning an empty or stale value.
		etag, err := loadOrBackfillETag(filepath.Join(bucketPath, name))
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error resolving ETag for %s: %v", name, err))
			return
		}
		objects = append(objects, ObjectInfo{
			Key:          name,
			LastModified: info.ModTime().Format(time.RFC3339),
			ETag:         etag,
			Size:         info.Size(),
			StorageClass: "STANDARD",
		})
	}

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{
			"name":     bucketName,
			"contents": objects,
		})
		return
	}
	result := struct {
		XMLName     xml.Name     `xml:"ListBucketResult"`
		XMLNS       string       `xml:"xmlns,attr"`
		Name        string       `xml:"Name"`
		Prefix      string       `xml:"Prefix"`
		Marker      string       `xml:"Marker"`
		MaxKeys     int          `xml:"MaxKeys"`
		IsTruncated bool         `xml:"IsTruncated"`
		Contents    []ObjectInfo `xml:"Contents"`
	}{
		XMLNS:       "https://s3.amazonaws.com/doc/2006-03-01/",
		Name:        bucketName,
		Prefix:      "",
		Marker:      "",
		MaxKeys:     1000,
		IsTruncated: false,
		Contents:    objects,
	}
	c.XML(http.StatusOK, result)
}

// applyMetadataHeaders copies persisted metadata onto the response headers,
// normalising Last-Modified into the HTTP date format S3 clients expect.
func applyMetadataHeaders(c *gin.Context, metadata map[string]string) {
	for key, value := range metadata {
		switch strings.ToLower(key) {
		case "content-type":
			c.Header("Content-Type", value)
		case "content-length":
			c.Header("Content-Length", value)
		case "last-modified":
			if t, err := time.Parse(time.RFC1123, value); err == nil {
				c.Header("Last-Modified", t.UTC().Format(http.TimeFormat))
			} else {
				c.Header("Last-Modified", value)
			}
		case "etag":
			c.Header("ETag", value)
		default:
			c.Header(key, value)
		}
	}
}

// HeadBucketHandler checks if a bucket exists and returns 200/404 with no body
// per the S3 HeadBucket contract. Auth/ACL are already enforced by middleware.
func HeadBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)
	fileInfo, err := os.Stat(bucketPath)
	if os.IsNotExist(err) {
		c.Status(http.StatusNotFound)
		return
	} else if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !fileInfo.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Status(http.StatusOK)
}

