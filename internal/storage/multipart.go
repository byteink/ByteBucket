package storage

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
)

// Multipart sentinels map 1:1 onto the S3 error codes the handler layer emits.
// Keeping them as exported sentinels (not stringly-typed) lets callers use
// errors.Is for dispatch without reparsing messages.
var (
	ErrNoSuchUpload     = errors.New("no such upload")
	ErrInvalidPart      = errors.New("invalid part")
	ErrInvalidPartOrder = errors.New("invalid part order")
	ErrInvalidPartRange = errors.New("part number out of range")
	ErrTooManyParts     = errors.New("too many parts")
)

// S3 multipart spec: part numbers are 1..10000 inclusive. Enforced here so
// higher layers cannot accidentally create unreachable parts on disk.
const (
	MinPartNumber = 1
	MaxPartNumber = 10000
)

// UploadsRoot is the directory under which in-progress multipart uploads live.
// Separate from ObjectsRoot so a bucket-level ReadDir never sees upload staging
// as phantom objects. Overridable for tests.
var UploadsRoot = "/data/uploads"

// MultipartUpload is the in-memory view of an in-progress upload. It mirrors
// the manifest.json shape on disk; see manifestPath / writeManifest.
type MultipartUpload struct {
	UploadID  string            `json:"uploadId"`
	Bucket    string            `json:"bucket"`
	Key       string            `json:"key"`
	Initiated time.Time         `json:"initiated"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// UploadedPart is the in-memory view of a single uploaded part. ETag is stored
// wire-quoted (same shape as single-PUT ETags) so callers that echo it back
// in XML do not need to quote at the edge.
type UploadedPart struct {
	PartNumber   int       `json:"partNumber"`
	ETag         string    `json:"etag"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
}

// uploadDir returns the on-disk root for a given upload. Layout:
//
//	<UploadsRoot>/<bucket>/<uploadID>/manifest.json
//	<UploadsRoot>/<bucket>/<uploadID>/<partNumber>       (raw bytes)
//
// Keyed by uploadID (a UUID) rather than key because S3 permits multiple
// concurrent uploads against the same key and we must not collide them.
func uploadDir(bucket, uploadID string) string {
	return filepath.Join(UploadsRoot, bucket, uploadID)
}

func manifestPath(bucket, uploadID string) string {
	return filepath.Join(uploadDir(bucket, uploadID), "manifest.json")
}

// partPath returns the part file path. Zero-padding keeps lexical sort order
// identical to numeric sort order, which matters for the Complete streaming
// step (we iterate ReadDir and must concatenate in partNumber order).
func partPath(bucket, uploadID string, partNumber int) string {
	return filepath.Join(uploadDir(bucket, uploadID), fmt.Sprintf("%05d", partNumber))
}

// CreateMultipartUpload allocates a new upload ID and persists the manifest.
// The returned MultipartUpload is also the shape callers should echo back in
// their wire response body (see handlers/multipart.go).
func CreateMultipartUpload(bucket, key string, metadata map[string]string) (*MultipartUpload, error) {
	if bucket == "" || key == "" {
		return nil, errors.New("bucket and key required")
	}
	up := &MultipartUpload{
		UploadID:  uuid.NewString(),
		Bucket:    bucket,
		Key:       key,
		Initiated: time.Now().UTC(),
		Metadata:  metadata,
	}
	if err := os.MkdirAll(uploadDir(bucket, up.UploadID), 0755); err != nil {
		return nil, err
	}
	if err := writeManifest(up); err != nil {
		// Best-effort cleanup: a stuck empty dir would be visible as a ghost
		// upload in ListMultipartUploads forever, which is worse than losing
		// the create entirely.
		_ = os.RemoveAll(uploadDir(bucket, up.UploadID))
		return nil, err
	}
	return up, nil
}

func writeManifest(up *MultipartUpload) error {
	data, err := json.MarshalIndent(up, "", "  ")
	if err != nil {
		return err
	}
	path := manifestPath(up.Bucket, up.UploadID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetMultipartUpload reads the manifest for a pending upload. Returns
// ErrNoSuchUpload when the upload dir is absent so handlers can map to a 404
// without parsing a stringly-typed error.
func GetMultipartUpload(bucket, key, uploadID string) (*MultipartUpload, error) {
	data, err := os.ReadFile(manifestPath(bucket, uploadID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSuchUpload
		}
		return nil, err
	}
	var up MultipartUpload
	if err := json.Unmarshal(data, &up); err != nil {
		return nil, err
	}
	// Defensive: manifest belongs to the caller's key. Mismatches should never
	// happen in prod (uploadID is unique) but catching it here prevents a
	// caller with one upload ID from completing against a different key.
	if up.Key != key || up.Bucket != bucket {
		return nil, ErrNoSuchUpload
	}
	return &up, nil
}

// UploadPart streams the part body to disk under a temp name, hashes it for
// the per-part ETag, and atomically renames into place. Concurrent writers for
// the same part number use the "last writer wins" rule that S3 documents:
// os.Rename is atomic on POSIX, so a torn read is impossible.
func UploadPart(bucket, key, uploadID string, partNumber int, body io.Reader) (*UploadedPart, error) {
	if partNumber < MinPartNumber || partNumber > MaxPartNumber {
		return nil, ErrInvalidPartRange
	}
	// Validate the upload exists before spending disk writes on a dead ID.
	if _, err := GetMultipartUpload(bucket, key, uploadID); err != nil {
		return nil, err
	}
	dst := partPath(bucket, uploadID, partNumber)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	h := md5.New()
	mw := io.MultiWriter(f, h)
	written, copyErr := io.Copy(mw, body)
	// Close before rename so buffers flush; surface a close error instead of
	// swallowing it behind a deferred close.
	if cerr := f.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(tmp)
		return nil, copyErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	etag := "\"" + hex.EncodeToString(h.Sum(nil)) + "\""
	return &UploadedPart{
		PartNumber:   partNumber,
		ETag:         etag,
		Size:         written,
		LastModified: time.Now().UTC(),
	}, nil
}

// ListParts returns the uploaded parts in ascending partNumber order. Used by
// the SDK during Complete to sanity-check what it uploaded and by the ListParts
// API directly.
func ListParts(bucket, key, uploadID string) ([]UploadedPart, error) {
	if _, err := GetMultipartUpload(bucket, key, uploadID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(uploadDir(bucket, uploadID))
	if err != nil {
		return nil, err
	}
	parts := make([]UploadedPart, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip the manifest and any in-flight temp renames; part files are the
		// zero-padded numeric names written by UploadPart.
		if name == "manifest.json" || strings.HasSuffix(name, ".tmp") {
			continue
		}
		pn, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		etag, err := partETag(bucket, uploadID, pn)
		if err != nil {
			return nil, err
		}
		parts = append(parts, UploadedPart{
			PartNumber:   pn,
			ETag:         etag,
			Size:         info.Size(),
			LastModified: info.ModTime().UTC(),
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

// partETag rehashes a stored part. Cheap relative to the upload itself and
// avoids maintaining a parallel etag sidecar that could drift from the bytes.
func partETag(bucket, uploadID string, partNumber int) (string, error) {
	f, err := os.Open(partPath(bucket, uploadID, partNumber))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "\"" + hex.EncodeToString(h.Sum(nil)) + "\"", nil
}

// CompleteMultipartUpload assembles the final object.
//
// S3 composite ETag contract (future reader WILL ask why):
//
//	finalETag = hex(md5(concat(md5_of_each_part))) + "-" + partCount
//
// It is NOT the MD5 of the final object's bytes. AWS SDKs validate this exact
// shape when a client supplies an expected ETag, so any drift here breaks
// SDK interop on large files.
//
// Ordering contract: expectedParts must be strictly ascending by PartNumber.
// S3 returns InvalidPartOrder otherwise; we mirror it.
func CompleteMultipartUpload(bucket, key, uploadID string, expectedParts []UploadedPart) (string, int64, error) {
	up, err := GetMultipartUpload(bucket, key, uploadID)
	if err != nil {
		return "", 0, err
	}
	if len(expectedParts) == 0 {
		return "", 0, ErrInvalidPart
	}
	// Validate monotonic part numbers up front — cheap and lets us fail before
	// touching the filesystem for part lookup.
	for i := 1; i < len(expectedParts); i++ {
		if expectedParts[i].PartNumber <= expectedParts[i-1].PartNumber {
			return "", 0, ErrInvalidPartOrder
		}
	}

	// Resolve each expected part by matching on-disk ETag. A mismatch is a
	// client-observable InvalidPart (the client committed to bytes we cannot
	// reproduce) — do not silently succeed.
	stored, err := ListParts(bucket, key, uploadID)
	if err != nil {
		return "", 0, err
	}
	storedByNum := make(map[int]UploadedPart, len(stored))
	for _, p := range stored {
		storedByNum[p.PartNumber] = p
	}
	for _, want := range expectedParts {
		got, ok := storedByNum[want.PartNumber]
		if !ok {
			return "", 0, ErrInvalidPart
		}
		if normalizeETag(got.ETag) != normalizeETag(want.ETag) {
			return "", 0, ErrInvalidPart
		}
	}

	// Stream-concatenate into the final object path using a temp file + rename
	// so a mid-concat crash never leaves a partial object at the target path.
	finalPath := filepath.Join(ObjectsRoot, bucket, key)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return "", 0, err
	}
	tmp := finalPath + ".mpu.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	composite := md5.New()
	var total int64
	for _, want := range expectedParts {
		n, partMD5, err := streamPart(out, bucket, uploadID, want.PartNumber)
		if err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return "", 0, err
		}
		composite.Write(partMD5)
		total += n
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}

	finalETag := fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(composite.Sum(nil)), len(expectedParts))

	// Write the object sidecar so GET/HEAD/LIST see a persistent ETag and
	// surface any user metadata captured at CreateMultipartUpload time.
	sidecar := make(map[string]string, len(up.Metadata)+3)
	for k, v := range up.Metadata {
		sidecar[k] = v
	}
	sidecar["ETag"] = finalETag
	sidecar["Content-Length"] = strconv.FormatInt(total, 10)
	sidecar["x-amz-multipart-parts"] = strconv.Itoa(len(expectedParts))
	raw, err := json.Marshal(sidecar)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(finalPath+".meta", raw, 0644); err != nil {
		return "", 0, err
	}

	// Best-effort staging cleanup. A failure here leaves orphan bytes in
	// UploadsRoot but does NOT fail the complete call: from the client's point
	// of view the object now exists; the staging dir becomes a janitorial
	// concern rather than a failure mode.
	_ = os.RemoveAll(uploadDir(bucket, uploadID))

	return finalETag, total, nil
}

// streamPart copies a part's bytes onto out while also returning the raw MD5
// bytes of the part. Returning the raw 16-byte digest (not the hex-quoted
// ETag) is deliberate: the composite ETag formula concatenates the RAW bytes
// of each part's MD5, then hashes that blob.
func streamPart(out io.Writer, bucket, uploadID string, partNumber int) (int64, []byte, error) {
	f, err := os.Open(partPath(bucket, uploadID, partNumber))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, ErrInvalidPart
		}
		return 0, nil, err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(out, h), f)
	if err != nil {
		return 0, nil, err
	}
	return n, h.Sum(nil), nil
}

// normalizeETag strips quotes so client- and server-supplied ETags compare
// equal regardless of whether the caller sent them wire-quoted.
func normalizeETag(s string) string {
	return strings.Trim(s, "\"")
}

// AbortMultipartUpload drops the staging directory. Idempotent: aborting a
// missing upload returns ErrNoSuchUpload so the handler can distinguish
// "already cleaned up" from "never existed" when the client wants to know.
func AbortMultipartUpload(bucket, key, uploadID string) error {
	if _, err := GetMultipartUpload(bucket, key, uploadID); err != nil {
		return err
	}
	return os.RemoveAll(uploadDir(bucket, uploadID))
}

// ListMultipartUploads enumerates in-progress uploads in a bucket. Order is
// not guaranteed by S3 and we do not fabricate one; callers that need a
// stable ordering sort by Initiated at the edge.
func ListMultipartUploads(bucket string) ([]*MultipartUpload, error) {
	bucketDir := filepath.Join(UploadsRoot, bucket)
	entries, err := os.ReadDir(bucketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*MultipartUpload, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(bucketDir, e.Name(), "manifest.json"))
		if err != nil {
			// Skip half-created or concurrently-aborted uploads rather than
			// breaking the entire listing on one bad entry.
			continue
		}
		var up MultipartUpload
		if err := json.Unmarshal(data, &up); err != nil {
			continue
		}
		out = append(out, &up)
	}
	return out, nil
}
