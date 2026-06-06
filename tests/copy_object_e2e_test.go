package tests

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestE2E_CopyObject_SDKCompat drives the real aws-sdk-go-v2 CopyObject call
// against the running container: it must duplicate the bytes under a new key
// while leaving the source intact, and the copy must be independently
// readable. Proves the x-amz-copy-source wire contract and CopyObjectResult
// parse as the SDK expects.
func TestE2E_CopyObject_SDKCompat(t *testing.T) {
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	ctx := context.TODO()
	const bucket = "copy-e2e-bkt"
	const body = "copy me verbatim"

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	t.Cleanup(func() {
		for _, k := range []string{"src.txt", "dst.txt"} {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		}
		_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("src.txt"), Body: strings.NewReader(body),
	}); err != nil {
		t.Fatalf("put src: %v", err)
	}

	if _, err := client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("dst.txt"),
		CopySource: aws.String(bucket + "/src.txt"),
	}); err != nil {
		t.Fatalf("CopyObject: %v", err)
	}

	// Source still present.
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("src.txt")}); err != nil {
		t.Fatalf("source missing after copy: %v", err)
	}
	// Destination readable and byte-identical.
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst.txt")})
	if err != nil {
		t.Fatalf("get dst: %v", err)
	}
	defer func() { _ = out.Body.Close() }()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != body {
		t.Fatalf("copy bytes mismatch: got %q want %q", got, body)
	}
}
