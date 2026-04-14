package tests

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// multipartPart pairs a part number with its ETag so the Complete body can be
// rebuilt from a slice in the order the server expects.
type multipartPart struct {
	Number int
	ETag   string
}

// createMultipartViaSigV4 initiates a multipart upload on the SigV4 surface
// and returns the parsed UploadId. Uses the locally-signed SigV4 helpers so
// failures land inside the test instead of inside the SDK.
func createMultipartViaSigV4(t *testing.T, bucket, key string) string {
	t.Helper()
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPost,
		path:   "/" + bucket + "/" + key,
		query:  map[string][]string{"uploads": {""}},
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse create: %v body=%s", err, body)
	}
	if out.UploadID == "" {
		t.Fatalf("no upload id: %s", body)
	}
	return out.UploadID
}

// uploadPartViaSigV4 streams one part over SigV4 and returns the part ETag.
func uploadPartViaSigV4(t *testing.T, bucket, key, uploadID string, partNum int, body []byte) string {
	t.Helper()
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPut,
		path:   "/" + bucket + "/" + key,
		query:  map[string][]string{"uploadId": {uploadID}, "partNumber": {fmt.Sprint(partNum)}},
		body:   body,
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload part %d: %v", partNum, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload part %d status=%d body=%s", partNum, resp.StatusCode, raw)
	}
	return resp.Header.Get("ETag")
}

// completeMultipartViaSigV4 finalises a multipart upload over SigV4 and
// returns the composite ETag as the server reports it.
func completeMultipartViaSigV4(t *testing.T, bucket, key, uploadID string, parts []multipartPart) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&buf, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.Number, p.ETag)
	}
	buf.WriteString("</CompleteMultipartUpload>")
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPost,
		path:   "/" + bucket + "/" + key,
		query:  map[string][]string{"uploadId": {uploadID}},
		body:   buf.Bytes(),
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		ETag string `xml:"ETag"`
	}
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse complete: %v body=%s", err, body)
	}
	return out.ETag
}

// compositeETagOf reimplements the S3 multipart ETag formula inside the test
// so a silent drift in the handler cannot pass under self-validation.
func compositeETagOf(parts [][]byte) string {
	h := md5.New()
	for _, p := range parts {
		sum := md5.Sum(p)
		h.Write(sum[:])
	}
	return fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(h.Sum(nil)), len(parts))
}

// TestE2E_Multipart_RoundtripViaSigV4 walks the full six-endpoint flow on
// port 9000 with real SigV4 signatures, then confirms GET returns the
// concatenation and that the composite ETag matches the contract form.
func TestE2E_Multipart_RoundtripViaSigV4(t *testing.T) {
	bucket := fmt.Sprintf("mp-sigv4-%d", time.Now().UnixNano())
	ensureBucket(t, adminCreds.AccessKeyID, adminCreds.SecretAccessKey, bucket)

	key := "obj.bin"
	parts := [][]byte{
		bytes.Repeat([]byte("A"), 10*1024),
		bytes.Repeat([]byte("B"), 10*1024),
		bytes.Repeat([]byte("C"), 5*1024),
	}

	uploadID := createMultipartViaSigV4(t, bucket, key)
	uploaded := make([]multipartPart, 0, len(parts))
	for i, p := range parts {
		pn := i + 1
		etag := uploadPartViaSigV4(t, bucket, key, uploadID, pn, p)
		if etag == "" {
			t.Fatalf("empty etag for part %d", pn)
		}
		uploaded = append(uploaded, multipartPart{Number: pn, ETag: etag})
	}

	gotETag := completeMultipartViaSigV4(t, bucket, key, uploadID, uploaded)
	wantETag := compositeETagOf(parts)
	if gotETag != wantETag {
		t.Fatalf("composite etag=%q want %q", gotETag, wantETag)
	}

	// GET must return the concatenation.
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method:    http.MethodGet,
		path:      "/" + bucket + "/" + key,
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	want := append(append([]byte{}, parts[0]...), append(parts[1], parts[2]...)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch: len got=%d want=%d", len(got), len(want))
	}
	if resp.Header.Get("ETag") != wantETag {
		t.Fatalf("GET etag=%q want %q", resp.Header.Get("ETag"), wantETag)
	}
}

// TestE2E_Multipart_RoundtripViaAdmin exercises the same six-endpoint flow
// on port 9001 under /s3 with JSON bodies and admin-header auth, proving
// cross-surface parity of the multipart implementation.
func TestE2E_Multipart_RoundtripViaAdmin(t *testing.T) {
	bucket := fmt.Sprintf("mp-admin-%d", time.Now().UnixNano())
	// Create bucket via admin surface.
	{
		req := adminRequest(t, http.MethodPut, "/s3/"+bucket, nil, "")
		resp := adminDo(t, req)
		_ = resp.Body.Close()
	}

	key := "obj.bin"
	// Create
	createResp := adminDo(t, adminRequest(t, http.MethodPost, "/s3/"+bucket+"/"+key+"?uploads", nil, ""))
	if createResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(createResp.Body)
		_ = createResp.Body.Close()
		t.Fatalf("admin create status=%d body=%s", createResp.StatusCode, b)
	}
	var createBody struct {
		UploadID string `json:"uploadId"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createBody); err != nil {
		t.Fatalf("admin create decode: %v", err)
	}
	_ = createResp.Body.Close()

	parts := [][]byte{
		bytes.Repeat([]byte("x"), 8*1024),
		bytes.Repeat([]byte("y"), 8*1024),
	}
	uploaded := make([]multipartPart, 0, len(parts))
	for i, p := range parts {
		pn := i + 1
		path := fmt.Sprintf("/s3/%s/%s?partNumber=%d&uploadId=%s", bucket, key, pn, createBody.UploadID)
		req := adminRequest(t, http.MethodPut, path, p, "application/octet-stream")
		resp := adminDo(t, req)
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("admin upload part %d status=%d body=%s", pn, resp.StatusCode, b)
		}
		etag := resp.Header.Get("ETag")
		_ = resp.Body.Close()
		uploaded = append(uploaded, multipartPart{Number: pn, ETag: etag})
	}

	// Complete via JSON body.
	bodyObj := map[string]any{"parts": []map[string]any{}}
	for _, p := range uploaded {
		bodyObj["parts"] = append(bodyObj["parts"].([]map[string]any),
			map[string]any{"partNumber": p.Number, "etag": p.ETag})
	}
	raw, _ := json.Marshal(bodyObj)
	compReq := adminRequest(t, http.MethodPost,
		"/s3/"+bucket+"/"+key+"?uploadId="+createBody.UploadID, raw, "application/json")
	compResp := adminDo(t, compReq)
	if compResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(compResp.Body)
		_ = compResp.Body.Close()
		t.Fatalf("admin complete status=%d body=%s", compResp.StatusCode, b)
	}
	var compOut struct {
		ETag string `json:"etag"`
	}
	if err := json.NewDecoder(compResp.Body).Decode(&compOut); err != nil {
		t.Fatalf("admin complete decode: %v", err)
	}
	_ = compResp.Body.Close()

	want := compositeETagOf(parts)
	if compOut.ETag != want {
		t.Fatalf("admin composite etag=%q want %q", compOut.ETag, want)
	}
}

// TestE2E_Multipart_AbortThenList verifies that aborting a multipart upload
// removes it from the bucket-level listing on the SigV4 surface.
func TestE2E_Multipart_AbortThenList(t *testing.T) {
	bucket := fmt.Sprintf("mp-abort-%d", time.Now().UnixNano())
	ensureBucket(t, adminCreds.AccessKeyID, adminCreds.SecretAccessKey, bucket)
	key := "aborted.bin"

	uploadID := createMultipartViaSigV4(t, bucket, key)
	uploadPartViaSigV4(t, bucket, key, uploadID, 1, []byte("hello"))

	// Abort
	abReq := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodDelete,
		path:   "/" + bucket + "/" + key,
		query:  map[string][]string{"uploadId": {uploadID}},
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	abResp, err := http.DefaultClient.Do(abReq)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	_ = abResp.Body.Close()
	if abResp.StatusCode != http.StatusNoContent {
		t.Fatalf("abort status=%d", abResp.StatusCode)
	}

	// ListMultipartUploads — should be empty
	listReq := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodGet,
		path:   "/" + bucket,
		query:  map[string][]string{"uploads": {""}},
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = listResp.Body.Close() }()
	body, _ := io.ReadAll(listResp.Body)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.StatusCode, body)
	}
	if strings.Contains(string(body), "<Upload>") {
		t.Fatalf("expected empty upload list, got: %s", body)
	}
}

// TestE2E_Multipart_AWSSDKCompat is the compatibility gold-standard:
// use the managed uploader (manager.Uploader.Upload) to push a 15 MiB file
// — which is larger than the 5 MiB default PartSize, forcing a real
// multipart sequence (CreateMultipartUpload, UploadPart x N,
// CompleteMultipartUpload) issued by the SDK. Then verify GET returns the
// identical bytes.
//
// If the SDK accepts our wire shape (upload-id echo, per-part ETag header,
// composite ETag from Complete), real-world S3 clients do too. This test
// is the single most important signal for "production ready" on the
// multipart path.
func TestE2E_Multipart_AWSSDKCompat(t *testing.T) {
	bucket := fmt.Sprintf("mp-sdk-%d", time.Now().UnixNano())
	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	_, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	key := "sdk-multipart.bin"
	// 15 MiB body with repeating pattern so mismatched reassembly is
	// immediately visible on the byte comparison. 15 MiB > 5 MiB default
	// part size, so the uploader emits at least three parts.
	const totalSize = 15 * 1024 * 1024
	payload := make([]byte, totalSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 5 * 1024 * 1024
		u.Concurrency = 3
	})
	_, err = uploader.Upload(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("sdk upload: %v", err)
	}

	// Verify via GetObject that the final bytes match.
	got, err := client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = got.Body.Close() }()
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body mismatch: got %d bytes, want %d", len(body), len(payload))
	}
	// Composite ETag: the SDK's uploader splits on PartSize boundaries; the
	// server's final ETag must have the -<partCount> suffix (SDKs surface the
	// ETag but don't enforce its shape on Upload — we still sanity-check).
	etag := aws.ToString(got.ETag)
	if !strings.Contains(etag, "-") {
		t.Fatalf("expected composite etag with -<partCount>, got %q", etag)
	}
}
