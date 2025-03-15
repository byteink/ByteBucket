package tests

import (
	"ByteBucket/internal/storage"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		config.WithEndpointResolver(aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
			return aws.Endpoint{URL: storageURL}, nil
		})),
	)
	if err != nil {
		panic(fmt.Sprintf("unable to load SDK config: %v", err))
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
}

// createRestrictedUser creates a restricted user (access to bucketA only) via the Admin API.
func createRestrictedUser(t *testing.T) (string, string) {
	t.Log("Creating restricted user via Admin API...")

	userPayload := map[string]interface{}{
		"acl": []map[string]interface{}{
			{
				"effect":  "Allow",
				"buckets": []string{"bucketA"},
				"actions": []string{"*"},
			},
			{
				"effect":  "Allow",
				"buckets": []string{"*"},
				"actions": []string{"s3:ListBuckets"},
			},
		},
	}

	body, err := json.Marshal(userPayload)
	if err != nil {
		t.Fatalf("Failed to marshal user payload: %v", err)
	}
	req, err := http.NewRequest("POST", adminURL+"/users", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Failed to create user: %s", resp.Status)
	}

	type CreateUserResponse struct {
		AccessKeyID     string            `json:"accessKeyID"`
		SecretAccessKey string            `json:"secretAccessKey"`
		ACL             []storage.ACLRule `json:"acl"`
	}

	var res CreateUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	t.Logf("Created user: %s", res.AccessKeyID)
	return res.AccessKeyID, res.SecretAccessKey
}

// deleteUser deletes a user via the Admin API.
func deleteUser(t *testing.T, accessKeyID string) {
	t.Log("Deleting user via Admin API...")

	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/users/%s", adminURL, accessKeyID), nil)
	if err != nil {
		t.Fatalf("Failed to create delete request: %v", err)
	}

	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to execute delete request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Failed to delete user: %s", resp.Status)
	}

	t.Logf("User %s deleted successfully", accessKeyID)
}

// testS3Operations performs S3 operations with the given client and credentials.
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

	// Get a presigned URL for the GET operation.
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
	defer resp.Body.Close()

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

// TestAdminAndUserAccess is the main test combining admin and restricted user scenarios.
func TestAdminAndUserAccess(t *testing.T) {
	// Step 1: Admin test using admin credentials.
	adminClient := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	testS3Operations(t, adminClient, "bucketA", "test.txt", "Admin test content", true)

	// Step 2: Create a restricted user.
	userAccessKey, userSecretKey := createRestrictedUser(t)
	userClient := createS3Client(userAccessKey, userSecretKey)

	// Step 3: Test restricted user on allowed bucket (bucketA).
	testS3Operations(t, userClient, "bucketA", "test.txt", "User allowed content", true)

	// Step 4: Test restricted user on denied bucket (bucketB) – expected to fail.
	testS3Operations(t, userClient, "bucketB", "test.txt", "User denied content", false)

	// Step 5: Delete the restricted user.
	deleteUser(t, userAccessKey)
}

// TestUserPermissions checks if a user is allowed to perform specific actions based on their ACL.
func TestUserPermissions(t *testing.T) {
	// Step 1: Create a restricted user with access to bucketA only.
	userAccessKey, userSecretKey := createRestrictedUser(t)
	userClient := createS3Client(userAccessKey, userSecretKey)

	// Step 2: Test restricted user on allowed bucket (bucketA) – expected to succeed.
	testS3Operations(t, userClient, "bucketA", "test-allowed.txt", "User allowed content", true)

	// Step 3: Test restricted user on denied bucket (bucketB) – expected to fail.
	testS3Operations(t, userClient, "bucketB", "test-denied.txt", "User denied content", false)

	// Step 4: Test listing buckets – expected to succeed.
	t.Log("Listing buckets")
	output, err := userClient.ListBuckets(context.TODO(), &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("Failed to list buckets: %v", err)
	}
	t.Logf("Buckets: %v", output.Buckets)

	// Step 5: Test restricted user trying to delete a bucket they don't have access to – expected to fail.
	t.Log("Attempting to delete bucketB")
	_, err = userClient.DeleteBucket(context.TODO(), &s3.DeleteBucketInput{Bucket: aws.String("bucketB")})
	if err == nil {
		t.Fatalf("Expected failure but succeeded in deleting bucket: bucketB")
	}

	// Step 6: Delete the restricted user.
	deleteUser(t, userAccessKey)
}
