package tests

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestE2E_DeleteObjects_SDKCompat drives the real aws-sdk-go-v2 DeleteObjects
// call against the running container. It proves the <Delete> request grammar
// and <DeleteResult> reply parse exactly as the AWS SDK expects, including the
// idempotent success for a key that does not exist.
func TestE2E_DeleteObjects_SDKCompat(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	ctx := context.TODO()
	const bucket = "batch-del-bkt"

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	keys := []string{"one.txt", "nested/two.txt"}
	for _, k := range keys {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(k), Body: nil,
		}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	out, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String("one.txt")},
				{Key: aws.String("nested/two.txt")},
				{Key: aws.String("never-existed.txt")}, // idempotent: still counts as deleted
			},
		},
	})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Fatalf("expected no per-key errors, got %+v", out.Errors)
	}
	if len(out.Deleted) != 3 {
		t.Fatalf("expected 3 deleted entries (incl. idempotent missing key), got %d", len(out.Deleted))
	}

	// The bucket must now be empty of the real keys.
	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.KeyCount != nil && *list.KeyCount != 0 {
		t.Fatalf("expected empty bucket after batch delete, KeyCount=%d", *list.KeyCount)
	}
}
