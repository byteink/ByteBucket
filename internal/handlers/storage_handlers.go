package handlers

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/gin-gonic/gin"
)

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
			RequestId:  "dummy-request-id",
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
	xmlResult := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		XMLNS   string   `xml:"xmlns,attr"`
		Owner   owner    `xml:"Owner"`
		Buckets struct {
			Bucket []Bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}{
		XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner: owner{ID: "dummy-owner-id", DisplayName: "dummy-owner"},
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
	// 500 rather than being swallowed by a deferred close.
	hasher := crc32.NewIEEE()
	multiWriter := io.MultiWriter(f, hasher)
	if _, err := io.Copy(multiWriter, c.Request.Body); err != nil {
		_ = f.Close()
		respondError(c, http.StatusInternalServerError, "InternalError", "Error saving file")
		return
	}
	if err := f.Close(); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error closing file")
		return
	}

	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, hasher.Sum32())
	checksumBase64 := base64.StdEncoding.EncodeToString(checksumBytes)

	metadata := make(map[string]string)
	for key, values := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-amz-meta-") {
			metadata[key] = values[0]
		}
	}
	metadata["x-amz-checksum-crc32"] = checksumBase64

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
		objects = append(objects, ObjectInfo{
			Key:          name,
			LastModified: info.ModTime().Format(time.RFC3339),
			ETag:         "\"dummy-etag\"",
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

	metadataPath := filePath + ".meta"
	if stat, err := os.Stat(metadataPath); err == nil && !stat.IsDir() {
		if data, err := os.ReadFile(metadataPath); err == nil {
			var metadata map[string]string
			if err := json.Unmarshal(data, &metadata); err == nil {
				applyMetadataHeaders(c, metadata)
			}
		}
	}

	c.File(filePath)
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

// DeleteObjectHandler deletes an object (file) from the specified bucket.
func DeleteObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join(objectsRoot, bucketName, objectKey)

	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error deleting object")
		return
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

// GetObjectMetadataHandler retrieves the metadata for an object.
// For HEAD requests, metadata is emitted as response headers (S3 HeadObject
// contract); for GET requests, it is returned as a JSON body for admin use.
func GetObjectMetadataHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	metadataPath := filepath.Join(objectsRoot, bucketName, objectKey+".meta")

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

	if c.Request.Method == http.MethodHead {
		applyMetadataHeaders(c, metadata)
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, metadata)
}
