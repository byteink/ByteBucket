package handlers

import (
	"ByteBucket/internal/storage"
	"ByteBucket/internal/util"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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

// CreateBucketHandler creates a new bucket (directory).
func CreateBucketHandler(c *gin.Context) {
	var req struct {
		BucketName string `json:"bucketName"`
	}
	if err := c.BindJSON(&req); err != nil || req.BucketName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	bucketPath := filepath.Join("/data/objects", req.BucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error creating bucket"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Bucket created"})
}

// ListBucketsHandler returns a stub list of buckets.
func ListBucketsHandler(c *gin.Context) {
	// In production, retrieve bucket list from DB.
	c.JSON(http.StatusOK, []string{"bucket1", "bucket2"})
}

// DeleteBucketHandler deletes a bucket.
func DeleteBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	bucketPath := filepath.Join("/data/objects", bucketName)
	if err := os.RemoveAll(bucketPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error deleting bucket"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Bucket %s deleted", bucketName)})
}

// UploadObjectHandler handles file uploads.
func UploadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File not provided"})
		return
	}
	bucketPath := filepath.Join("/data/objects", bucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error creating bucket directory"})
		return
	}
	dstPath := filepath.Join(bucketPath, file.Filename)
	if err := c.SaveUploadedFile(file, dstPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving file"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Object uploaded"})
}

// DownloadObjectHandler serves an object (authenticated).
func DownloadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	objectKey := c.Param("objectKey")
	// objectKey comes with a leading slash due to wildcard; clean it.
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join("/data/objects", bucketName, objectKey)
	c.File(filePath)
}

// DeleteObjectHandler deletes an object.
func DeleteObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join("/data/objects", bucketName, objectKey)
	if err := os.Remove(filePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error deleting object"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Object deleted"})
}

// ListObjectsHandler lists objects in a bucket.
func ListObjectsHandler(c *gin.Context) {
	bucketName := c.Param("bucketName")
	bucketPath := filepath.Join("/data/objects", bucketName)
	files, err := os.ReadDir(bucketPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error reading bucket"})
		return
	}
	var objects []string
	for _, f := range files {
		objects = append(objects, f.Name())
	}
	c.JSON(http.StatusOK, objects)
}

// PresignUploadHandler returns a dummy presigned URL.
func PresignUploadHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"presignedUrl": "http://localhost:9000/dummy-upload-url"})
}

// PresignDownloadHandler returns a dummy presigned URL.
func PresignDownloadHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"presignedUrl": "http://localhost:9000/dummy-download-url"})
}

// PublicDownloadObjectHandler serves public objects if ACL is "public-read".
func PublicDownloadObjectHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	meta, err := storage.GetObjectMetadata(bucket, objectKey)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Object not found"})
		return
	}
	if meta.ACL != "public-read" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access Denied"})
		return
	}
	filePath := filepath.Join("/data/objects", bucket, objectKey)
	c.File(filePath)
}

// CreateUserHandler auto-generates a new ACCESS_KEY_ID and SECRET_ACCESS_KEY.
func CreateUserHandler(c *gin.Context) {
	var req struct {
		ACL string `json:"acl"`
	}
	if err := c.BindJSON(&req); err != nil || req.ACL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: missing ACL"})
		return
	}

	accessKeyID := util.GenerateRandomString(20, util.AccessKeyCharset)
	secretAccessKey := util.GenerateRandomString(40, util.SecretAccessKeyCharset)

	encrypted, err := storage.Encrypt(secretAccessKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error encrypting secret"})
		return
	}

	user := storage.User{
		AccessKeyID:     accessKeyID,
		EncryptedSecret: encrypted,
		ACL:             req.ACL,
	}
	if err := storage.CreateUser(&user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error creating user: %v", err)})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"ACCESS_KEY_ID":     accessKeyID,
		"SECRET_ACCESS_KEY": secretAccessKey,
	})
}

// ListUsersHandler returns a list of users (without secret details).
func ListUsersHandler(c *gin.Context) {
	users, err := storage.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error listing users"})
		return
	}
	var infos []gin.H
	for _, u := range users {
		infos = append(infos, gin.H{
			"accessKeyID": u.AccessKeyID,
			"acl":         u.ACL,
		})
	}
	c.JSON(http.StatusOK, infos)
}
