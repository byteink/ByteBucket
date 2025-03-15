package handlers

import (
	"encoding/xml"
	"fmt"
	"github.com/goccy/go-json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler returns a simple JSON status.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// CreateBucketHandler creates a new bucket (directory) and returns an XML response compatible with S3 SDK expectations.
func CreateBucketHandler(c *gin.Context) {
	// Retrieve bucket name from URL parameter (S3 API passes the bucket name in the URL)
	bucketName := c.Param("bucket")
	if bucketName == "" {
		c.XML(http.StatusBadRequest, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Bucket name required",
		})
		return
	}

	bucketPath := filepath.Join("/data/objects", bucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error creating bucket",
		})
		return
	}

	// Return a minimal XML response conforming to the S3 CreateBucketResult structure
	c.XML(http.StatusOK, struct {
		XMLName  xml.Name `xml:"CreateBucketResult"`
		Location string   `xml:"Location"`
	}{
		Location: fmt.Sprintf("http://%s/%s", c.Request.Host, bucketName),
	})
}

// ListBucketsHandler returns a list of buckets in an XML response.
func ListBucketsHandler(c *gin.Context) {
	basePath := "/data/objects"
	entries, err := os.ReadDir(basePath)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: fmt.Sprintf("Error listing buckets: %v", err),
		})
		return
	}

	type Bucket struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	}
	var buckets []Bucket
	for _, entry := range entries {
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				c.XML(http.StatusInternalServerError, struct {
					XMLName xml.Name `xml:"Error"`
					Message string   `xml:"Message"`
				}{
					Message: fmt.Sprintf("Error getting info for bucket %s: %v", entry.Name(), err),
				})
				return
			}
			buckets = append(buckets, Bucket{
				Name:         entry.Name(),
				CreationDate: info.ModTime().Format(time.RFC3339),
			})
		}
	}

	result := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		XMLNS   string   `xml:"xmlns,attr"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		Buckets struct {
			Bucket []Bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}{
		XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner: struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		}{
			ID:          "dummy-owner-id",
			DisplayName: "dummy-owner",
		},
	}
	result.Buckets.Bucket = buckets
	c.XML(http.StatusOK, result)
}

// DeleteBucketHandler deletes a bucket.
func DeleteBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		c.XML(http.StatusBadRequest, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Bucket name required",
		})
		return
	}

	bucketPath := filepath.Join("/data/objects", bucketName)
	if bucketPath == "/data/objects" {
		c.XML(http.StatusBadRequest, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Cannot delete base directory",
		})
		return
	}

	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		c.XML(http.StatusNotFound, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Bucket not found",
		})
		return
	}

	if err := os.RemoveAll(bucketPath); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error deleting bucket",
		})
		return
	}

	c.Status(http.StatusNoContent)
}

// UploadObjectHandler handles object uploads by reading the raw request body.
func UploadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	// Ensure the bucket directory exists.
	bucketPath := filepath.Join("/data/objects", bucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error creating bucket directory",
		})
		return
	}

	// Determine the destination file path.
	dstPath := filepath.Join(bucketPath, objectKey)
	// Create or truncate the file.
	f, err := os.Create(dstPath)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error creating file",
		})
		return
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			c.XML(http.StatusInternalServerError, struct {
				XMLName xml.Name `xml:"Error"`
				Message string   `xml:"Message"`
			}{
				Message: "Error closing file",
			})
		}
	}(f)

	// Copy the raw request body to the file.
	if _, err := io.Copy(f, c.Request.Body); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error saving file",
		})
		return
	}

	// Parse metadata from headers.
	metadata := make(map[string]string)
	for key, values := range c.Request.Header {
		if strings.HasPrefix(key, "X-Amz-Meta-") {
			metadata[key] = values[0]
		}
	}

	// Store metadata in a JSON file.
	metadataPath := dstPath + ".meta"
	metadataFile, err := os.Create(metadataPath)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error creating metadata file",
		})
		return
	}
	defer metadataFile.Close()

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error encoding metadata",
		})
		return
	}

	if _, err := metadataFile.Write(metadataJSON); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error writing metadata",
		})
		return
	}

	c.Status(http.StatusNoContent)
}

// ListObjectsHandler lists objects in a bucket and returns an XML response conforming to the S3 List Objects API.
func ListObjectsHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	bucketPath := filepath.Join("/data/objects", bucketName)
	entries, err := os.ReadDir(bucketPath)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error reading bucket",
		})
		return
	}

	type ObjectInfo struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}
	var objects []ObjectInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			objects = append(objects, ObjectInfo{
				Key:          entry.Name(),
				LastModified: info.ModTime().Format(time.RFC3339),
				ETag:         "\"dummy-etag\"",
				Size:         info.Size(),
				StorageClass: "STANDARD",
			})
		}
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
	// Clean the object key to remove any redundant separators or relative paths.
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join("/data/objects", bucketName, objectKey)

	// Check if the file exists.
	if _, err := os.Stat(filePath); err != nil {
		c.XML(http.StatusNotFound, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Object not found",
		})
		return
	}

	// Determine the metadata file path (stored alongside the object).
	metadataPath := filePath + ".meta"
	if stat, err := os.Stat(metadataPath); err == nil && !stat.IsDir() {
		metadataFile, err := os.Open(metadataPath)
		if err == nil {
			defer metadataFile.Close()

			// Decode the JSON metadata.
			var metadata map[string]string
			if err := json.NewDecoder(metadataFile).Decode(&metadata); err == nil {
				// Set standard metadata headers and any custom headers.
				for key, value := range metadata {
					switch strings.ToLower(key) {
					case "content-type":
						c.Header("Content-Type", value)
					case "content-length":
						c.Header("Content-Length", value)
					case "last-modified":
						// Ensure Last-Modified is in the proper HTTP format.
						if t, err := time.Parse(time.RFC1123, value); err == nil {
							c.Header("Last-Modified", t.UTC().Format(http.TimeFormat))
						} else {
							c.Header("Last-Modified", value)
						}
					case "etag":
						c.Header("ETag", value)
					default:
						// Any other metadata is exposed as custom metadata.
						c.Header(key, value)
					}
				}
			}
		}
	}

	// Serve the file. The metadata headers above will be included in the response.
	c.File(filePath)
}

// DeleteObjectHandler deletes an object (file) from the specified bucket.
func DeleteObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join("/data/objects", bucketName, objectKey)
	if err := os.Remove(filePath); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error deleting object",
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// GetObjectMetadataHandler retrieves the metadata for an object.
// It supports S3 SDK compatibility by handling HEAD requests.
func GetObjectMetadataHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	// Determine the metadata file path.
	metadataPath := filepath.Join("/data/objects", bucketName, objectKey+".meta")

	// Check if the metadata file exists.
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		c.XML(http.StatusNotFound, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Metadata not found",
		})
		return
	}

	// Read the metadata file.
	metadataFile, err := os.Open(metadataPath)
	if err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error opening metadata file",
		})
		return
	}
	defer metadataFile.Close()

	// Decode the JSON metadata.
	var metadata map[string]string
	decoder := json.NewDecoder(metadataFile)
	if err := decoder.Decode(&metadata); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error decoding metadata",
		})
		return
	}

	// For HEAD requests, set the metadata as HTTP headers (no body)
	if c.Request.Method == http.MethodHead {
		// Iterate over the metadata and set headers.
		for key, value := range metadata {
			switch strings.ToLower(key) {
			case "content-type":
				c.Header("Content-Type", value)
			case "content-length":
				c.Header("Content-Length", value)
			case "last-modified":
				// Ensure Last-Modified is in a valid HTTP format.
				// Attempt to parse the value as time if possible.
				if t, err := time.Parse(time.RFC1123, value); err == nil {
					c.Header("Last-Modified", t.UTC().Format(http.TimeFormat))
				} else {
					c.Header("Last-Modified", value)
				}
			case "etag":
				c.Header("ETag", value)
			default:
				// Set other metadata as custom headers.
				c.Header(key, value)
			}
		}
		c.Status(http.StatusOK)
		return
	}

	// For GET requests (or others), return the metadata as JSON.
	c.JSON(http.StatusOK, metadata)
}
