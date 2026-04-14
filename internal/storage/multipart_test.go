package storage

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// withTempRoots redirects ObjectsRoot and UploadsRoot at isolated temp dirs
// for the life of the test. Restoring both on cleanup keeps the package-level
// vars from leaking between tests run under -count=1+.
func withTempRoots(t *testing.T) (string, string) {
	t.Helper()
	objDir := t.TempDir()
	upDir := t.TempDir()
	origObjects, origUploads := ObjectsRoot, UploadsRoot
	ObjectsRoot, UploadsRoot = objDir, upDir
	t.Cleanup(func() {
		ObjectsRoot, UploadsRoot = origObjects, origUploads
	})
	return objDir, upDir
}

// compositeETag reimplements the S3 multipart ETag formula so the production
// code path and this test cannot silently drift in lockstep. A single-source
// of truth would mask the bug the test is supposed to catch.
func compositeETag(parts [][]byte) string {
	h := md5.New()
	for _, p := range parts {
		sum := md5.Sum(p)
		h.Write(sum[:])
	}
	return fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(h.Sum(nil)), len(parts))
}

func TestMultipart_RoundtripCompositeETag(t *testing.T) {
	objDir, _ := withTempRoots(t)
	bucket, key := "bkt", "obj.bin"
	if err := os.MkdirAll(filepath.Join(objDir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	up, err := CreateMultipartUpload(bucket, key, map[string]string{"x-amz-meta-foo": "bar"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	parts := [][]byte{
		bytes.Repeat([]byte("a"), 1024),
		bytes.Repeat([]byte("b"), 2048),
		bytes.Repeat([]byte("c"), 512),
	}
	uploaded := make([]UploadedPart, 0, len(parts))
	// Upload out of order on purpose: complete must reassemble ascending.
	order := []int{2, 1, 3}
	for _, pn := range order {
		p, err := UploadPart(bucket, key, up.UploadID, pn, bytes.NewReader(parts[pn-1]))
		if err != nil {
			t.Fatalf("upload part %d: %v", pn, err)
		}
		uploaded = append(uploaded, *p)
	}

	// Complete expects ascending order — rebuild the slice accordingly.
	expected := []UploadedPart{
		find(uploaded, 1), find(uploaded, 2), find(uploaded, 3),
	}
	etag, size, err := CompleteMultipartUpload(bucket, key, up.UploadID, expected)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	wantETag := compositeETag(parts)
	if etag != wantETag {
		t.Fatalf("etag = %q; want %q", etag, wantETag)
	}
	wantSize := int64(len(parts[0]) + len(parts[1]) + len(parts[2]))
	if size != wantSize {
		t.Fatalf("size = %d; want %d", size, wantSize)
	}

	// Final object bytes are the concatenation in partNumber order.
	got, err := os.ReadFile(filepath.Join(objDir, bucket, key))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	want := append(append([]byte{}, parts[0]...), append(parts[1], parts[2]...)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("concatenation mismatch: got %d bytes, want %d", len(got), len(want))
	}

	// Staging must be gone.
	if _, err := os.Stat(uploadDir(bucket, up.UploadID)); !os.IsNotExist(err) {
		t.Fatalf("upload dir still present: err=%v", err)
	}
}

func find(parts []UploadedPart, n int) UploadedPart {
	for _, p := range parts {
		if p.PartNumber == n {
			return p
		}
	}
	panic(fmt.Sprintf("part %d not found", n))
}

func TestMultipart_AbortRemovesStaging(t *testing.T) {
	objDir, _ := withTempRoots(t)
	bucket, key := "bkt", "gone.bin"
	if err := os.MkdirAll(filepath.Join(objDir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	up, err := CreateMultipartUpload(bucket, key, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := UploadPart(bucket, key, up.UploadID, 1, bytes.NewReader([]byte("hi"))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := AbortMultipartUpload(bucket, key, up.UploadID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, err := os.Stat(uploadDir(bucket, up.UploadID)); !os.IsNotExist(err) {
		t.Fatalf("upload dir still present: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(objDir, bucket, key)); !os.IsNotExist(err) {
		t.Fatalf("final object unexpectedly created")
	}
}

func TestMultipart_CompleteMissingPart(t *testing.T) {
	withTempRoots(t)
	bucket, key := "bkt", "k"
	up, err := CreateMultipartUpload(bucket, key, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ObjectsRoot, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fake := []UploadedPart{{PartNumber: 1, ETag: "\"deadbeef\""}}
	_, _, err = CompleteMultipartUpload(bucket, key, up.UploadID, fake)
	if !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("err = %v; want ErrInvalidPart", err)
	}
}

func TestMultipart_CompleteNoSuchUpload(t *testing.T) {
	withTempRoots(t)
	_, _, err := CompleteMultipartUpload("bkt", "k", "missing-id", []UploadedPart{{PartNumber: 1, ETag: "\"x\""}})
	if !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("err = %v; want ErrNoSuchUpload", err)
	}
}

func TestMultipart_PartNumberRange(t *testing.T) {
	withTempRoots(t)
	up, err := CreateMultipartUpload("bkt", "k", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, pn := range []int{0, -1, MaxPartNumber + 1} {
		if _, err := UploadPart("bkt", "k", up.UploadID, pn, bytes.NewReader(nil)); !errors.Is(err, ErrInvalidPartRange) {
			t.Fatalf("pn=%d err=%v; want ErrInvalidPartRange", pn, err)
		}
	}
}

func TestMultipart_ListAndGet(t *testing.T) {
	withTempRoots(t)
	up, err := CreateMultipartUpload("bkt", "k", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ups, err := ListMultipartUploads("bkt")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ups) != 1 || ups[0].UploadID != up.UploadID {
		t.Fatalf("list = %+v; want one entry with UploadID=%s", ups, up.UploadID)
	}
	got, err := GetMultipartUpload("bkt", "k", up.UploadID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UploadID != up.UploadID {
		t.Fatalf("get uploadID = %s; want %s", got.UploadID, up.UploadID)
	}
	// Get with wrong key must 404 — defence against cross-key completion.
	if _, err := GetMultipartUpload("bkt", "other", up.UploadID); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("wrong-key err = %v; want ErrNoSuchUpload", err)
	}
}
