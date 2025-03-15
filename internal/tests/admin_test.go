package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// API endpoints
const storageURL = "http://localhost:9000"
const adminURL = "http://localhost:9001"

// Admin credentials (provided for testing)
var adminCreds = struct {
	AccessKeyID     string
	SecretAccessKey string
}{
	AccessKeyID:     "APE6at7CMFvJaEJjnmbC",
	SecretAccessKey: "40ylGQ3lRaxE/SQFRZrHZY+e+XD7CBMVa8ioUsAO",
}

// createS3Client creates an S3 client with the given credentials.
func createS3Client(accessKey, secretKey string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithBaseEndpoint(storageURL),
	)
	if err != nil {
		panic(fmt.Sprintf("unable to load SDK config: %v", err))
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
}

// createRestrictedUser creates a restricted user (access to bucketA only) via the Admin API.
// The withListPermission flag indicates whether to include an ACL rule for the ListBuckets action.
func createRestrictedUser(withListPermission bool) (string, string) {
	// Prepare ACL rules payload.
	var aclRules []map[string]interface{}
	aclRules = append(aclRules, map[string]interface{}{
		"effect":  "Allow",
		"buckets": []string{"bucketA"},
		"actions": []string{"*"},
	})
	if withListPermission {
		aclRules = append(aclRules, map[string]interface{}{
			"effect":  "Allow",
			"buckets": []string{"*"},
			"actions": []string{"s3:ListBuckets"},
		})
	}

	userPayload := map[string]interface{}{
		"acl": aclRules,
	}

	body, _ := json.Marshal(userPayload)
	req, err := http.NewRequest("POST", adminURL+"/users", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			panic(err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		panic(fmt.Sprintf("Failed to create user: %s", resp.Status))
	}

	// Assuming the response is a JSON object with keys "accessKeyID" and "secretAccessKey".
	var user map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		panic(fmt.Sprintf("Failed to decode response: %v", err))
	}

	accessKeyID, ok := user["accessKeyID"].(string)
	if !ok {
		panic("accessKeyID is not a string")
	}
	secretAccessKey, ok := user["secretAccessKey"].(string)
	if !ok {
		panic("secretAccessKey is not a string")
	}
	return accessKeyID, secretAccessKey
}

// deleteUser deletes a user via the Admin API.
func deleteUser(t *testing.T, accessKeyID string) {
	t.Log("Deleting user via Admin API...")

	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/users/%s", adminURL, accessKeyID), nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Failed to delete user: %s", resp.Status)
	}

	t.Logf("User %s deleted successfully", accessKeyID)
}

// testS3Operations performs S3 operations with the given client on a specified bucket.
func testS3Operations(t *testing.T, client *s3.Client, bucket, key, content string, shouldSucceed bool) {
	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if shouldSucceed && err != nil {
		t.Fatalf("Expected success but failed to create bucket: %v", err)
	} else if !shouldSucceed && err == nil {
		t.Fatalf("Expected failure but succeeded in creating bucket: %s", bucket)
	}

	// Skip further tests if creation was supposed to fail.
	if !shouldSucceed {
		return
	}

	t.Logf("Uploading object to %s/%s", bucket, key)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(content)),
	})
	if err != nil {
		t.Fatalf("Failed to upload object: %v", err)
	}

	t.Log("Downloading object using presigned URL...")
	getObjCmd := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	presignClient := s3.NewPresignClient(client)
	presignedResp, err := presignClient.PresignGetObject(context.TODO(), getObjCmd, func(o *s3.PresignOptions) {
		o.Expires = 15 * time.Minute
	})
	if err != nil {
		t.Fatalf("Failed to presign URL: %v", err)
	}

	resp, err := http.Get(presignedResp.URL)
	if err != nil {
		t.Fatalf("Failed to fetch object using presigned URL: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Fatalf("Failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected response status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read object content: %v", err)
	}

	t.Logf("Downloaded content: %s", string(body))

	// Cleanup: Delete object and bucket.
	t.Logf("Deleting object %s", key)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Logf("Warning: failed to delete object: %v", err)
	}

	t.Logf("Deleting bucket %s", bucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// testListBuckets performs the ListBuckets operation and verifies whether it should succeed.
func testListBuckets(t *testing.T, client *s3.Client, expectSuccess bool) {
	t.Log("Testing ListBuckets operation...")
	_, err := client.ListBuckets(context.TODO(), &s3.ListBucketsInput{})
	if expectSuccess && err != nil {
		t.Fatalf("Expected ListBuckets to succeed but got error: %v", err)
	} else if !expectSuccess && err == nil {
		t.Fatalf("Expected ListBuckets to fail, but it succeeded")
	} else if err != nil {
		t.Logf("ListBuckets failed as expected: %v", err)
	} else {
		t.Log("ListBuckets succeeded as expected")
	}
}

// TestAdminAndUserAccess tests S3 operations with both admin and restricted users.
func TestAdminAndUserAccess(t *testing.T) {
	// Step 1: Admin test using admin credentials.
	adminClient := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	testS3Operations(t, adminClient, "bucketA", "test.txt", "Admin test content", true)

	// Step 2: Restricted user WITH ListBuckets permission.
	userAccessKey, userSecretKey := createRestrictedUser(true)
	userClientWithList := createS3Client(userAccessKey, userSecretKey)
	testS3Operations(t, userClientWithList, "bucketA", "test.txt", "User allowed content", true)
	testListBuckets(t, userClientWithList, true)
	deleteUser(t, userAccessKey)

	// Step 3: Restricted user WITHOUT ListBuckets permission.
	userAccessKey2, userSecretKey2 := createRestrictedUser(false)
	userClientWithoutList := createS3Client(userAccessKey2, userSecretKey2)
	testListBuckets(t, userClientWithoutList, false)
	deleteUser(t, userAccessKey2)
}

// TestInvalidCredentials verifies that operations using invalid credentials fail.
func TestInvalidCredentials(t *testing.T) {
	t.Log("Testing invalid credentials...")
	invalidClient := createS3Client("invalid", "invalid")
	_, err := invalidClient.ListBuckets(context.TODO(), &s3.ListBucketsInput{})
	if err == nil {
		t.Fatal("Expected error when listing buckets with invalid credentials, got none")
	}
	t.Logf("ListBuckets failed as expected with invalid credentials: %v", err)
}

// TestRestrictedUserBucketCreation ensures that a restricted user cannot create buckets outside their allowed ACL.
func TestRestrictedUserBucketCreation(t *testing.T) {
	t.Log("Testing restricted user bucket creation for unauthorized bucket...")
	accessKey, secretKey := createRestrictedUser(false) // ACL without extra permissions.
	client := createS3Client(accessKey, secretKey)

	// Attempt to create a bucket not in the allowed ACL (bucketB).
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String("bucketB")})
	if err == nil {
		t.Fatal("Expected failure when creating unauthorized bucket, but succeeded")
	}
	t.Logf("Creating unauthorized bucket failed as expected: %v", err)
	deleteUser(t, accessKey)
}

// TestPresignedURLExpiration checks that a presigned URL with a short expiration fails after expiry.
func TestPresignedURLExpiration(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucket := "bucket-expiration-test"
	key := "test.txt"
	content := "Presigned URL expiration test content"

	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	t.Logf("Uploading object to %s/%s", bucket, key)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(content)),
	})
	if err != nil {
		t.Fatalf("Failed to upload object: %v", err)
	}

	// Generate presigned URL with a 2-second expiration.
	presignClient := s3.NewPresignClient(client)
	presignedResp, err := presignClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignOptions) {
		o.Expires = 2 * time.Second
	})
	if err != nil {
		t.Fatalf("Failed to presign URL: %v", err)
	}

	t.Log("Sleeping for 3 seconds to let presigned URL expire")
	time.Sleep(3 * time.Second)

	resp, err := http.Get(presignedResp.URL)
	if err != nil {
		t.Fatalf("Failed to fetch object using presigned URL: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Fatalf("Failed to close response body: %v", err)
		}
	}(resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("Expected presigned URL to expire, but it succeeded")
	} else {
		t.Logf("Presigned URL expired as expected with status: %s", resp.Status)
	}

	// Cleanup.
	t.Logf("Deleting object %s", key)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Logf("Warning: failed to delete object: %v", err)
	}
	t.Logf("Deleting bucket %s", bucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// TestDeleteNonExistentBucket verifies that deleting a bucket that does not exist produces an error.
func TestDeleteNonExistentBucket(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	nonExistentBucket := "nonexistent-bucket-12345"
	t.Logf("Attempting to delete non-existent bucket: %s", nonExistentBucket)
	_, err := client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(nonExistentBucket)})
	if err == nil {
		t.Fatal("Expected error when deleting non-existent bucket, but got none")
	}
	t.Logf("Deleting non-existent bucket failed as expected: %v", err)
}

// TestConcurrentBucketCreation creates and deletes multiple buckets concurrently.
func TestConcurrentBucketCreation(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucketPrefix := "concurrent-bucket-"
	numBuckets := 5

	var wg sync.WaitGroup
	errCh := make(chan error, numBuckets)

	for i := 0; i < numBuckets; i++ {
		wg.Add(1)
		bucketName := fmt.Sprintf("%s%d", bucketPrefix, i)
		go func(b string) {
			defer wg.Done()
			t.Logf("Creating bucket: %s", b)
			_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(b)})
			if err != nil {
				errCh <- fmt.Errorf("failed to create bucket %s: %v", b, err)
				return
			}
			// Clean up: delete bucket.
			_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(b)})
			if err != nil {
				errCh <- fmt.Errorf("failed to delete bucket %s: %v", b, err)
				return
			}
			errCh <- nil
		}(bucketName)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Log("Concurrent bucket creation and deletion succeeded")
}

// TestLargeFileUpload tests the upload of a moderately large object.
func TestLargeFileUpload(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucket := "bucket-large-upload"
	key := "largefile.txt"
	// Generate a payload of ~5MB.
	payloadSize := 5 * 1024 * 1024
	payload := bytes.Repeat([]byte("A"), payloadSize)

	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	t.Logf("Uploading large object to %s/%s", bucket, key)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("Failed to upload large object: %v", err)
	}

	// Cleanup.
	t.Logf("Deleting object %s", key)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Logf("Warning: failed to delete object: %v", err)
	}
	t.Logf("Deleting bucket %s", bucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}
