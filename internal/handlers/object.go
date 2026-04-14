package handlers

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ByteBucket/internal/middleware"

	"github.com/goccy/go-json"

	"github.com/gin-gonic/gin"
)

// UploadObjectHandler handles object uploads by reading the raw request body.
func UploadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	bucketPath := filepath.Join(objectsRoot, bucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating bucket directory")
		return
	}

	dstPath := filepath.Join(bucketPath, objectKey)
	parentDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating parent directories")
		return
	}

	f, err := os.Create(dstPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating file")
		return
	}
	// Close explicitly after streaming so an fsync/close failure surfaces as a
	// 500 rather than being swallowed by a deferred close. The MD5 hasher is
	// fed by the same MultiWriter as the CRC32 so we compute the S3 ETag in
	// one pass without re-reading the file from disk.
	crcHasher := crc32.NewIEEE()
	md5Hasher := md5.New()
	multiWriter := io.MultiWriter(f, crcHasher, md5Hasher)
	written, err := io.Copy(multiWriter, c.Request.Body)
	if err != nil {
		_ = f.Close()
		respondError(c, http.StatusInternalServerError, "InternalError", "Error saving file")
		return
	}
	if err := f.Close(); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error closing file")
		return
	}

	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, crcHasher.Sum32())
	checksumBase64 := base64.StdEncoding.EncodeToString(checksumBytes)
	etag := formatETag(md5Hasher)

	metadata := make(map[string]string)
	for key, values := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-amz-meta-") {
			metadata[key] = values[0]
		}
	}
	metadata["x-amz-checksum-crc32"] = checksumBase64
	metadata[etagMetaKey] = etag
	metadata["Content-Length"] = strconv.FormatInt(written, 10)

	metadataPath := dstPath + ".meta"
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error encoding metadata")
		return
	}
	if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error writing metadata")
		return
	}

	// Credit the new object's bytes against the bucket gauge. Best-effort
	// delta — not recomputed at startup — so the value should be treated
	// as a trendline, not an authoritative size report.
	middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Add(float64(written))

	// ETag is part of the S3 PutObject response contract; SDKs read it and
	// optionally verify against a client-side Content-MD5.
	c.Header("ETag", etag)
	c.Status(http.StatusOK)
}

// DownloadObjectHandler serves an object (file) from the specified bucket.
// It also sets metadata headers from the associated metadata file (if available)
// to be compatible with the S3 SDK GetObject response.
func DownloadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join(objectsRoot, bucketName, objectKey)

	if _, err := os.Stat(filePath); err != nil {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Object not found")
		return
	}

	// Backfill the ETag before emitting headers so legacy objects — written
	// before ETag persistence — still return a correct, wire-shaped value.
	etag, err := loadOrBackfillETag(filePath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error resolving ETag")
		return
	}

	metadataPath := filePath + ".meta"
	if stat, err := os.Stat(metadataPath); err == nil && !stat.IsDir() {
		if data, err := os.ReadFile(metadataPath); err == nil {
			var metadata map[string]string
			if err := json.Unmarshal(data, &metadata); err == nil {
				applyMetadataHeaders(c, metadata)
			}
		}
	}
	// Always emit the canonical ETag; applyMetadataHeaders may have written
	// nothing for pre-migration objects whose sidecar lacked the key.
	c.Header("ETag", etag)

	c.File(filePath)
}

// DeleteObjectHandler deletes an object (file) from the specified bucket.
func DeleteObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join(objectsRoot, bucketName, objectKey)

	// Capture size before removal so the per-bucket byte gauge can be
	// decremented symmetrically with UploadObjectHandler's Add. A Stat
	// error (e.g. concurrent delete) is non-fatal — we simply skip the
	// gauge update rather than fail the request.
	var removedBytes int64
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		removedBytes = info.Size()
	}

	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error deleting object")
		return
	}
	if removedBytes > 0 {
		middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Sub(float64(removedBytes))
	}

	// Best-effort metadata sidecar cleanup; a missing sidecar is not an error.
	_ = os.Remove(filePath + ".meta")

	// Collapse now-empty parent directories up to the bucket root. Stop on
	// the first non-empty dir or any error so we never remove unrelated
	// content or the bucket root itself.
	parentDir := filepath.Dir(filePath)
	bucketDir := filepath.Join(objectsRoot, bucketName)
	for parentDir != bucketDir && parentDir != "/" {
		entries, err := os.ReadDir(parentDir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(parentDir); err != nil {
			break
		}
		parentDir = filepath.Dir(parentDir)
	}

	c.Status(http.StatusNoContent)
}

// GetObjectMetadataHandler retrieves the metadata for an object.
// For HEAD requests, metadata is emitted as response headers (S3 HeadObject
// contract); for GET requests, it is returned as a JSON body for admin use.
func GetObjectMetadataHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	objectPath := filepath.Join(objectsRoot, bucketName, objectKey)
	metadataPath := objectPath + ".meta"

	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Metadata not found")
		return
	}

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error opening metadata file")
		return
	}

	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error decoding metadata")
		return
	}

	// Backfill the ETag in-place so HEAD responses and the JSON body always
	// include it, even for objects predating ETag persistence.
	if tag := metadata[etagMetaKey]; tag == "" {
		backfilled, err := loadOrBackfillETag(objectPath)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError", "Error resolving ETag")
			return
		}
		metadata[etagMetaKey] = backfilled
	}

	if c.Request.Method == http.MethodHead {
		applyMetadataHeaders(c, metadata)
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, metadata)
}
