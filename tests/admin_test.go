package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

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
	req, err := http.NewRequest("POST", adminURL+"/api/users", bytes.NewReader(body))
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

	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/api/users/%s", adminURL, accessKeyID), nil)
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

// TestGetObjectMetadata tests the retrieval of object metadata.
func TestGetObjectMetadata(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucket := "bucket-metadata-test"
	key := "test.txt"
	content := "Test content"
	// S3 converts metadata keys to lowercase.
	expectedMetadata := map[string]string{
		"author": "Test Author",
		"type":   "Test Type",
	}

	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	t.Logf("Uploading object to %s/%s with metadata", bucket, key)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		Body:     bytes.NewReader([]byte(content)),
		Metadata: map[string]string{"author": "Test Author", "type": "Test Type"},
	})
	if err != nil {
		t.Fatalf("Failed to upload object: %v", err)
	}

	// Retrieve metadata using HeadObject.
	t.Log("Retrieving object metadata using HeadObject...")
	headResp, err := client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("Failed to retrieve head object: %v", err)
	}

	if !reflect.DeepEqual(headResp.Metadata, expectedMetadata) {
		t.Fatalf("HeadObject metadata mismatch. Expected: %v, Got: %v", expectedMetadata, headResp.Metadata)
	}

	// Retrieve metadata using GetObject.
	t.Log("Retrieving object metadata using GetObject...")
	getResp, err := client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("Failed to retrieve object: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Fatalf("Failed to close object body: %v", err)
		}
	}(getResp.Body)

	if !reflect.DeepEqual(getResp.Metadata, expectedMetadata) {
		t.Fatalf("GetObject metadata mismatch. Expected: %v, Got: %v", expectedMetadata, getResp.Metadata)
	}

	// Verify object content.
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("Failed to read object body: %v", err)
	}
	if string(body) != content {
		t.Fatalf("Object content mismatch. Expected: %s, Got: %s", content, string(body))
	}

	// Cleanup.
	t.Logf("Deleting object %s", key)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Logf("Warning: failed to delete object: %v", err)
	}
	t.Logf("Deleting bucket %s", bucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// TestHeadBucket tests the HeadBucket operation on existing and non-existing buckets.
func TestHeadBucket(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	existingBucket := "bucket-head-test"
	restrictedBucket := "bucket-restricted-head-test"
	nonExistentBucket := "nonexistent-bucket-12345"

	// Create buckets for testing
	t.Logf("Creating bucket: %s", existingBucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	t.Logf("Creating bucket: %s", restrictedBucket)
	_, err = client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(restrictedBucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Test HeadBucket on existing bucket - should succeed
	t.Logf("Testing HeadBucket on existing bucket: %s", existingBucket)
	_, err = client.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Fatalf("Expected HeadBucket to succeed on existing bucket but got error: %v", err)
	}
	t.Log("HeadBucket succeeded on existing bucket as expected")

	// Test HeadBucket on non-existent bucket - should fail
	t.Logf("Testing HeadBucket on non-existent bucket: %s", nonExistentBucket)
	_, err = client.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(nonExistentBucket),
	})
	if err == nil {
		t.Fatal("Expected HeadBucket to fail on non-existent bucket, but it succeeded")
	}
	t.Logf("HeadBucket failed on non-existent bucket as expected: %v", err)

	// Create a restricted user with custom permissions for this test
	// Prepare ACL rules payload with access to bucket-head-test
	var aclRules []map[string]interface{}
	aclRules = append(aclRules, map[string]interface{}{
		"effect":  "Allow",
		"buckets": []string{existingBucket},
		"actions": []string{"*"},
	})

	userPayload := map[string]interface{}{
		"acl": aclRules,
	}

	body, _ := json.Marshal(userPayload)
	req, err := http.NewRequest("POST", adminURL+"/api/users", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Failed to create user: %s", resp.Status)
	}

	// Parse the response to get user credentials
	var user map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	err = resp.Body.Close()
	if err != nil {
		return
	}

	userAccessKey, ok := user["accessKeyID"].(string)
	if !ok {
		t.Fatal("accessKeyID is not a string")
	}
	userSecretKey, ok := user["secretAccessKey"].(string)
	if !ok {
		t.Fatal("secretAccessKey is not a string")
	}

	restrictedClient := createS3Client(userAccessKey, userSecretKey)

	// Test HeadBucket on authorized bucket - should succeed
	t.Logf("Testing restricted user HeadBucket on authorized bucket: %s", existingBucket)
	_, err = restrictedClient.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Fatalf("Expected restricted user HeadBucket to succeed on authorized bucket but got error: %v", err)
	}
	t.Log("Restricted user HeadBucket succeeded on authorized bucket as expected")

	// Test HeadBucket on unauthorized bucket - should fail
	t.Logf("Testing restricted user HeadBucket on unauthorized bucket: %s", restrictedBucket)
	_, err = restrictedClient.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(restrictedBucket),
	})
	if err == nil {
		t.Fatal("Expected restricted user HeadBucket to fail on unauthorized bucket, but it succeeded")
	}
	t.Logf("Restricted user HeadBucket failed on unauthorized bucket as expected: %v", err)

	// Clean up the restricted user
	deleteUser(t, userAccessKey)

	// Cleanup buckets
	t.Logf("Deleting bucket %s", existingBucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}

	t.Logf("Deleting bucket %s", restrictedBucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(restrictedBucket),
	})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// TestHeadDefaultBucket specifically tests the HeadBucket operation on a bucket named "default"
// to ensure it doesn't have any special handling issues.
func TestHeadDefaultBucket(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	defaultBucket := "default"

	// Create a bucket named "default" for testing
	t.Logf("Creating bucket: %s", defaultBucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(defaultBucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Test HeadBucket on the default bucket - should succeed
	t.Logf("Testing HeadBucket on bucket: %s", defaultBucket)
	_, err = client.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(defaultBucket),
	})
	if err != nil {
		t.Fatalf("Expected HeadBucket to succeed on 'default' bucket but got error: %v", err)
	}
	t.Log("HeadBucket succeeded on 'default' bucket as expected")

	// Cleanup
	t.Logf("Deleting bucket %s", defaultBucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(defaultBucket),
	})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// TestCreateExistingBucket verifies that creating a bucket that already exists
// returns a BucketAlreadyOwnedByYou error, similar to AWS S3
func TestCreateExistingBucket(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	existingBucket := "existing-bucket-test"

	// Create a bucket for testing
	t.Logf("Creating bucket: %s", existingBucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Try to create the same bucket again
	t.Logf("Attempting to create the same bucket again: %s", existingBucket)
	_, err = client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(existingBucket),
	})

	// Verify we get the correct error
	if err == nil {
		t.Fatal("Expected error when creating existing bucket, but got none")
	}

	// Check for BucketAlreadyOwnedByYou error
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		t.Logf("Error code: %s", apiErr.ErrorCode())
		if apiErr.ErrorCode() != "BucketAlreadyOwnedByYou" {
			t.Fatalf("Expected BucketAlreadyOwnedByYou error, got: %s", apiErr.ErrorCode())
		}
		t.Logf("Creating existing bucket failed with expected error: %v", err)
	} else {
		t.Fatalf("Expected an API error, got: %v", err)
	}

	// Clean up
	t.Logf("Deleting bucket %s", existingBucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(existingBucket),
	})
	if err != nil {
		t.Logf("Warning: failed to delete bucket: %v", err)
	}
}

// TestDeleteNonExistentObject verifies that deleting an object that doesn't exist returns success (204 No Content).
// This matches S3's behavior where deleting a non-existent object is considered a successful operation.
func TestDeleteNonExistentObject(t *testing.T) {
	// Create S3 client
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)

	// Create a bucket for testing
	bucket := fmt.Sprintf("nonexistent-obj-test-%d", time.Now().UnixNano())
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Attempt to delete a non-existent object
	nonExistentKey := "this/object/does/not/exist.txt"
	deleteOutput, err := client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(nonExistentKey),
	})

	// Assert no error is returned
	if err != nil {
		t.Fatalf("Expected no error when deleting non-existent object, got: %v", err)
	}

	t.Logf("DeleteObject response for non-existent object: %+v", deleteOutput)

	// Clean up the bucket
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Logf("Warning: Failed to delete bucket during cleanup: %v", err)
	}
}

// TestDeleteObjectMetadata verifies that metadata files are correctly deleted when objects are deleted.
func TestDeleteObjectMetadata(t *testing.T) {
	// Create S3 client
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)

	// Create a bucket for testing
	bucket := fmt.Sprintf("metadata-delete-test-%d", time.Now().UnixNano())
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Create an object with metadata
	key := "test-file-with-metadata.txt"
	content := "This is a test file with metadata"

	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(content)),
		ContentType: aws.String("text/plain"),
		Metadata: map[string]string{
			"test-key": "test-value",
		},
	})
	if err != nil {
		t.Fatalf("Failed to upload object with metadata: %v", err)
	}

	// Verify object and metadata exist by getting object metadata
	headResp, err := client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("Failed to get object metadata: %v", err)
	}
	if headResp.Metadata["test-key"] != "test-value" {
		t.Fatalf("Expected metadata test-key=test-value, got %s", headResp.Metadata["test-key"])
	}

	// Delete the object
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("Failed to delete object: %v", err)
	}

	// Verify object and metadata don't exist anymore
	_, err = client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		t.Fatalf("Object should not exist after deletion")
	}

	// Check if error indicates the object doesn't exist
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() != "NotFound" {
			t.Fatalf("Expected NotFound error, got %s", apiErr.ErrorCode())
		}
	} else {
		t.Fatalf("Expected APIError with NotFound code, got %v", err)
	}

	// Clean up the bucket
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Logf("Warning: Failed to delete bucket during cleanup: %v", err)
	}
}

// TestDeleteObjectBehavior tests various scenarios for DeleteObject operation
// to ensure it matches S3's behavior for existing and non-existent objects
func TestDeleteObjectBehavior(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucket := fmt.Sprintf("delete-object-test-%d", time.Now().UnixNano())

	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Test Case 1: Delete an existing object
	existingKey := "existing-object.txt"
	content := "This is an existing object"

	t.Logf("Uploading object to %s/%s", bucket, existingKey)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(existingKey),
		Body:        bytes.NewReader([]byte(content)),
		ContentType: aws.String("text/plain"),
		Metadata: map[string]string{
			"test-key": "test-value",
		},
	})
	if err != nil {
		t.Fatalf("Failed to upload existing object: %v", err)
	}

	// Verify object exists
	_, err = client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(existingKey),
	})
	if err != nil {
		t.Fatalf("Expected object to exist, but got error: %v", err)
	}

	// Delete existing object
	t.Logf("Deleting existing object %s/%s", bucket, existingKey)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(existingKey),
	})
	if err != nil {
		t.Fatalf("Failed to delete existing object: %v", err)
	}

	// Verify object was deleted
	_, err = client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(existingKey),
	})
	if err == nil {
		t.Fatalf("Object should have been deleted but still exists")
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() != "NotFound" {
			t.Fatalf("Expected NotFound error for deleted object, got: %s", apiErr.ErrorCode())
		}
	} else {
		t.Fatalf("Unexpected error type: %v", err)
	}

	// Test Case 2: Delete a non-existent object
	nonExistentKey := "this/object/does/not/exist.txt"

	// Delete non-existent object - should succeed with 204 No Content
	t.Logf("Deleting non-existent object %s/%s", bucket, nonExistentKey)
	deleteResp, err := client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(nonExistentKey),
	})
	if err != nil {
		t.Fatalf("Expected no error when deleting non-existent object, got: %v", err)
	}

	t.Logf("DeleteObject response for non-existent object: %v", deleteResp)

	// Test Case 3: Create and delete object with nested path
	nestedKey := "nested/path/to/object.txt"
	nestedContent := "This is a nested object"

	t.Logf("Uploading nested object to %s/%s", bucket, nestedKey)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(nestedKey),
		Body:   bytes.NewReader([]byte(nestedContent)),
	})
	if err != nil {
		t.Fatalf("Failed to upload nested object: %v", err)
	}

	// Delete nested object
	t.Logf("Deleting nested object %s/%s", bucket, nestedKey)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(nestedKey),
	})
	if err != nil {
		t.Fatalf("Failed to delete nested object: %v", err)
	}

	// Verify nested object was deleted
	_, err = client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(nestedKey),
	})
	if err == nil {
		t.Fatalf("Nested object should have been deleted but still exists")
	}

	// Test Case 4: Delete the same non-existent object again
	t.Logf("Deleting already deleted object again %s/%s", bucket, existingKey)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(existingKey),
	})
	if err != nil {
		t.Fatalf("Expected no error when deleting already deleted object, got: %v", err)
	}

	// Clean up bucket
	t.Logf("Cleaning up bucket %s", bucket)
	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Logf("Warning: Failed to delete bucket during cleanup: %v", err)
	}
}

// TestDeleteObjectMetadataCleanup tests that metadata files are properly cleaned up
// when objects are deleted, including the case where only a metadata file exists
func TestDeleteObjectMetadataCleanup(t *testing.T) {
	// Setup: Create a bucket, object with metadata, and verify everything is created properly
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	bucket := fmt.Sprintf("metadata-cleanup-test-%d", time.Now().UnixNano())

	t.Logf("Creating bucket: %s", bucket)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}

	// Create object with rich metadata
	objectKey := "metadata-test-object.txt"
	content := "This is a test object with metadata"

	t.Logf("Creating object with metadata: %s/%s", bucket, objectKey)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader([]byte(content)),
		ContentType: aws.String("text/plain"),
		Metadata: map[string]string{
			"author":       "Test Author",
			"description":  "Test Description",
			"version":      "1.0",
			"content-info": "Test metadata with multiple fields",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create object with metadata: %v", err)
	}

	// Verify metadata exists via HeadObject
	headResp, err := client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		t.Fatalf("Failed to get object metadata: %v", err)
	}

	if len(headResp.Metadata) == 0 {
		t.Fatalf("Expected metadata to exist, but none found")
	}

	t.Logf("Object created with metadata: %v", headResp.Metadata)

	// Test: Delete the object
	t.Logf("Deleting object with metadata: %s/%s", bucket, objectKey)
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		t.Fatalf("Failed to delete object: %v", err)
	}

	// Verify object and metadata are gone
	_, err = client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err == nil {
		t.Fatalf("Object should have been deleted but still exists")
	}

	// Test: Create object again to verify metadata is fully removed
	t.Logf("Creating new object at same key: %s/%s", bucket, objectKey)
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader([]byte("New content")),
		ContentType: aws.String("text/plain"),
		Metadata: map[string]string{
			"new-metadata": "This should be the only metadata",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create new object: %v", err)
	}

	// Verify only new metadata exists
	headResp2, err := client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		t.Fatalf("Failed to get new object metadata: %v", err)
	}

	if len(headResp2.Metadata) != 1 || headResp2.Metadata["new-metadata"] != "This should be the only metadata" {
		t.Fatalf("Expected only new metadata to exist, got: %v", headResp2.Metadata)
	}

	t.Logf("New object has correct metadata: %v", headResp2.Metadata)

	// Clean up
	t.Logf("Cleaning up test objects and bucket")
	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		t.Logf("Warning: Failed to delete object during cleanup: %v", err)
	}

	_, err = client.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Logf("Warning: Failed to delete bucket during cleanup: %v", err)
	}
}
