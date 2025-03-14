package handlers

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
)

// HomeHandler returns a simple welcome message.
func HomeHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "Welcome to ByteBucket!"})
}

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
			Message: "Error listing buckets",
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
				continue
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
	bucketName := c.Param("bucketName")
	bucketPath := filepath.Join("/data/objects", bucketName)
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

// UploadObjectHandler handles file uploads.
func UploadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	file, err := c.FormFile("file")
	if err != nil {
		c.XML(http.StatusBadRequest, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "File not provided",
		})
		return
	}
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
	dstPath := filepath.Join(bucketPath, file.Filename)
	if err := c.SaveUploadedFile(file, dstPath); err != nil {
		c.XML(http.StatusInternalServerError, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Error saving file",
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
		XMLNS:       "http://s3.amazonaws.com/doc/2006-03-01/",
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
func DownloadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	// Clean the object key to remove any redundant separators or relative paths
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join("/data/objects", bucketName, objectKey)
	// Check if the file exists
	if _, err := os.Stat(filePath); err != nil {
		c.XML(http.StatusNotFound, struct {
			XMLName xml.Name `xml:"Error"`
			Message string   `xml:"Message"`
		}{
			Message: "Object not found",
		})
		return
	}
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
