package handlers

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/goccy/go-json"

	"github.com/gin-gonic/gin"
)

// UploadObjectHandler handles object uploads by reading the raw request body.
func UploadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := filepath.Clean(c.Param("objectKey"))

	// Resolve the canned ACL (if any) up-front so an invalid value rejects
	// the request before we spend IO writing the body.
	cannedACL, ok := resolveCannedACL(c)
	if !ok {
		return
	}

	metadata, mErr := collectUserMetadataChecked(c)
	if mErr != nil {
		respondError(c, http.StatusBadRequest, "MetadataTooLarge",
			"x-amz-meta-* headers exceed 2 KiB total")
		return
	}
	metadata["Content-Type"] = contentTypeOrOctet(c)
	if cannedACL != "" {
		metadata["acl"] = cannedACL
	}

	dstPath := filepath.Join(objectsRoot, bucketName, objectKey)

	// Optimistic-concurrency preconditions: If-None-Match:* (create-only) and
	// If-Match:<etag> (overwrite-only-if-unchanged) are evaluated against the
	// current on-disk object before we commit any bytes.
	if status, handled := evaluatePutConditional(c, dstPath); handled {
		if status == http.StatusInternalServerError {
			respondError(c, status, "InternalError", "Error resolving object ETag")
		} else {
			respondError(c, status, "PreconditionFailed", "At least one precondition failed")
		}
		return
	}

	etag, written, err := finalizeObjectWrite(bucketName, dstPath, c.Request.Body, metadata)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error saving object")
		return
	}
	middleware.RecordObjectUpload(bucketName, written)

	// If the upload set an explicit canned ACL, audit it as if it were a
	// PutObjectAcl call. The visible transition is "default -> <canned>";
	// helpful when scoping which uploads opened up which objects.
	if cannedACL != "" {
		auditACLChange(c, "object", bucketName, objectKey, storage.ACLPrivate, cannedACL)
	}

	// ETag is part of the S3 PutObject response contract; SDKs read it and
	// optionally verify against a client-side Content-MD5.
	c.Header("ETag", etag)
	c.Status(http.StatusOK)
}

// resolveCannedACL returns the validated canned ACL from the x-amz-acl header,
// or "" when the header is absent (the object then inherits the bucket ACL).
// On an unsupported value it writes the error response and returns ok=false.
func resolveCannedACL(c *gin.Context) (string, bool) {
	h := c.GetHeader("x-amz-acl")
	if h == "" {
		return "", true
	}
	normalized, err := storage.NormalizeCannedACL(h)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Unsupported x-amz-acl value")
		return "", false
	}
	return normalized, true
}

// contentTypeOrOctet returns the request Content-Type, defaulting to
// application/octet-stream so a stored object always carries a non-sniffable
// type (paired with nosniff at GET time).
func contentTypeOrOctet(c *gin.Context) string {
	if ct := strings.TrimSpace(c.GetHeader("Content-Type")); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// finalizeObjectWrite streams src into the object at dstPath, computing the S3
// ETag and CRC32 in a single pass, then persists the .meta sidecar built from
// meta plus the computed checksum/etag/length and credits the per-bucket byte
// gauge. The bytes are written to a temp file and renamed into place, so a
// crash never leaves a partial object and a copy whose source IS the
// destination cannot truncate its own input mid-read. meta must already carry
// Content-Type and any acl entry. Returns the ETag and bytes written.
func finalizeObjectWrite(bucketName, dstPath string, src io.Reader, meta map[string]string) (string, int64, error) {
	// Serialize with any concurrent write/delete of the same key so the object
	// file and its .meta sidecar are committed as a consistent pair.
	defer lockObjectPath(dstPath)()

	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", 0, err
	}

	etag, crc32b64, written, err := streamToObject(dir, dstPath, src)
	if err != nil {
		return "", 0, err
	}

	meta["x-amz-checksum-crc32"] = crc32b64
	meta[etagMetaKey] = etag
	meta["Content-Length"] = strconv.FormatInt(written, 10)

	metadataJSON, err := json.Marshal(meta)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(dstPath+".meta", metadataJSON, 0644); err != nil {
		return "", 0, err
	}

	// Best-effort gauge delta — a trendline, not an authoritative size report.
	middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Add(float64(written))
	return etag, written, nil
}

// streamToObject writes src to a temp file in dir, computing the S3 ETag and
// CRC32 in a single pass, optionally fsyncs the data, then atomically renames
// it into dstPath (and fsyncs dir when durable). Returns the ETag, the base64
// CRC32, and the byte count.
func streamToObject(dir, dstPath string, src io.Reader) (string, string, int64, error) {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	// The MD5 and CRC32 hashers share the MultiWriter so the S3 ETag and the
	// checksum are computed in one pass without re-reading from disk.
	crcHasher := crc32.NewIEEE()
	md5Hasher := md5.New()
	written, err := io.Copy(io.MultiWriter(tmp, crcHasher, md5Hasher), src)
	if err != nil {
		_ = tmp.Close()
		return "", "", 0, err
	}
	// Flush data to stable storage before the rename when durability is on, so
	// an acknowledged write survives power loss; skipped when the operator has
	// traded durability for throughput.
	durable := syncWritesEnabled()
	if durable {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return "", "", 0, err
		}
	}
	// Match the 0644 an os.Create would have produced; CreateTemp uses 0600.
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return "", "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", "", 0, err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return "", "", 0, err
	}
	committed = true

	// fsync the parent directory so the rename itself is durable: without it a
	// crash can lose the directory entry even though the file data was synced.
	if durable {
		if err := fsyncDir(dir); err != nil {
			return "", "", 0, err
		}
	}

	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, crcHasher.Sum32())
	return formatETag(md5Hasher), base64.StdEncoding.EncodeToString(checksumBytes), written, nil
}

// fsyncDir flushes a directory entry to stable storage so a rename into it is
// durable across a crash.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// DownloadObjectHandler serves an object (file) from the specified bucket.
// It also sets metadata headers from the associated metadata file (if available)
// to be compatible with the S3 SDK GetObject response.
func DownloadObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)
	filePath := filepath.Join(objectsRoot, bucketName, objectKey)

	info, err := os.Stat(filePath)
	if err != nil {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Object not found")
		return
	}

	// Browsers must not MIME-sniff stored bytes. Without nosniff an attacker
	// with write access to a public-read bucket could upload an HTML payload
	// labelled as image/jpeg and have any viewer execute it inline. Set
	// before emitting the file so even an early flush carries it.
	c.Header("X-Content-Type-Options", "nosniff")

	// Backfill the ETag before emitting headers so legacy objects — written
	// before ETag persistence — still return a correct, wire-shaped value.
	etag, err := loadOrBackfillETag(filePath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error resolving ETag")
		return
	}

	metadataPath := filePath + ".meta"
	if stat, err := os.Stat(metadataPath); err == nil && !stat.IsDir() {
		if data, err := os.ReadFile(metadataPath); err == nil {
			var metadata map[string]string
			if err := json.Unmarshal(data, &metadata); err == nil {
				applyMetadataHeaders(c, metadata)
			}
		}
	}
	// Always emit the canonical ETag; applyMetadataHeaders may have written
	// nothing for pre-migration objects whose sidecar lacked the key.
	c.Header("ETag", etag)

	// Drop the sidecar Content-Length: ServeContent recomputes it from the
	// open file and, on a 206, must report the slice length — not the full
	// object size that applyMetadataHeaders copied from the sidecar. Leaving
	// it would emit a wrong Content-Length on partial reads — and, critically,
	// on a precondition short-circuit (304/412) it would advertise a body
	// length the empty/error response never sends, hanging the client.
	c.Writer.Header().Del("Content-Length")

	// Honour read preconditions (If-Match/If-None-Match/If-(Un)Modified-Since)
	// before streaming. This must precede the Range handling so a failed
	// precondition wins over a range request, per RFC 7233.
	if applyGetConditional(c, etag, info.ModTime()) {
		return
	}

	// Pre-classify any Range header before handing the request to
	// ServeContent. The stdlib parser is RFC-correct for satisfiable ranges
	// but treats a malformed header as a 416 "invalid range"; S3 (RFC 7233
	// §3.1) instead ignores a header it cannot understand and returns the
	// full 200 body. It also omits Content-Range when an overflowing first-
	// byte-pos fails integer parsing. We normalise both edges here so the
	// surface matches S3 exactly, then let ServeContent stream the slice.
	switch classifyRange(c.GetHeader("Range"), info.Size()) {
	case rangeIgnore:
		// Strip the header so ServeContent serves the unconditional 200 body.
		c.Request.Header.Del("Range")
	case rangeUnsatisfiable:
		// 416 with "bytes */total" and the range advertisement, matching what
		// ServeContent emits for an overlapping-but-out-of-bounds range — but
		// applied uniformly, including the overflow case ServeContent drops.
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Range", "bytes */"+strconv.FormatInt(info.Size(), 10))
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		// Flush now: gin defers WriteHeader until the first body write, but a
		// 416 carries no body, so without this the recorded status stays 200.
		c.Writer.WriteHeaderNow()
		return
	case rangeSatisfiable:
		// Leave the header in place; ServeContent re-parses it and produces
		// the 206 with the correct Content-Range/Content-Length.
	}

	f, err := os.Open(filePath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error opening object")
		return
	}
	defer func() { _ = f.Close() }()

	// ServeContent owns satisfiable single/multi-range streaming: 206 status,
	// Content-Range, Accept-Ranges, and If-Range. It streams the requested
	// slice via Seek rather than buffering the object, so a partial read never
	// allocates the whole file. Content-Type is left untouched because we
	// already set it from the sidecar (an empty value would otherwise trigger
	// content sniffing inside ServeContent). The name argument is only used for
	// that sniffing fallback, so it is irrelevant here. modtime drives
	// Last-Modified/If-Modified-Since and If-Range.
	http.ServeContent(c.Writer, c.Request, "", info.ModTime(), f)

	// Record real download activity per bucket. c.Writer.Size() is the bytes
	// actually written — the full object on a 200, the slice on a 206 — so a
	// ranged read is counted at its true cost.
	if n := c.Writer.Size(); n > 0 {
		middleware.RecordObjectDownload(bucketName, int64(n))
	}
}

// rangeClass is the result of pre-validating a Range request header against a
// known object size, decoupling our S3-correct semantics from the stdlib's.
type rangeClass int

const (
	// rangeIgnore: no Range header, an unknown unit, or a malformed value.
	// Per RFC 7233 §3.1 the server ignores it and returns the full body.
	rangeIgnore rangeClass = iota
	// rangeSatisfiable: a syntactically valid byte range that overlaps the
	// object; ServeContent will emit the 206 slice.
	rangeSatisfiable
	// rangeUnsatisfiable: a syntactically valid range whose first-byte-pos is
	// at or beyond the object size (including values too large to fit int64);
	// the caller emits 416 with "bytes */total".
	rangeUnsatisfiable
)

// classifyRange decides how a single-range "bytes=" header should be handled
// for an object of the given size. It accepts only the single-range forms S3
// serves (start-end, start-, -suffix); a multi-range or unparseable value is
// ignored so the full body is returned rather than risking a stdlib 416.
//
// All arithmetic is bounded by size and overflow-safe: out-of-int64 numbers
// surface as a ParseInt error and route to rangeIgnore/rangeUnsatisfiable
// rather than wrapping, so a hostile "bytes=0-99999999999999999999" can never
// drive a negative length, an out-of-bounds seek, or an allocation blowup.
func classifyRange(header string, size int64) rangeClass {
	const prefix = "bytes="
	if header == "" || !strings.HasPrefix(header, prefix) {
		return rangeIgnore
	}
	spec := strings.TrimSpace(header[len(prefix):])
	// Reject multi-range up front: the comma form is valid HTTP but S3 only
	// honours a single range, and ServeContent would otherwise build a
	// multipart/byteranges body we never want to emit here.
	if spec == "" || strings.Contains(spec, ",") {
		return rangeIgnore
	}

	startStr, endStr, ok := strings.Cut(spec, "-")
	if !ok {
		return rangeIgnore
	}
	startStr = strings.TrimSpace(startStr)
	endStr = strings.TrimSpace(endStr)

	if startStr == "" {
		return classifySuffixRange(endStr, size)
	}
	return classifyOffsetRange(startStr, endStr, size)
}

// classifySuffixRange handles the "bytes=-N" suffix form (last N bytes). A
// zero or non-numeric/overflowing suffix is malformed and ignored; a positive
// suffix always overlaps a non-empty object (the server clamps it to size), so
// it is satisfiable, while an empty object can satisfy no suffix.
func classifySuffixRange(suffix string, size int64) rangeClass {
	n, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || n <= 0 {
		return rangeIgnore
	}
	if size == 0 {
		return rangeUnsatisfiable
	}
	return rangeSatisfiable
}

// classifyOffsetRange handles "bytes=start-" and "bytes=start-end". A
// non-numeric/overflowing or negative start is malformed (ignored); a start at
// or past size is unsatisfiable (416); otherwise the end is validated and the
// range is satisfiable.
func classifyOffsetRange(startStr, endStr string, size int64) rangeClass {
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		// A numeric start that overflows int64 (ErrRange) is unambiguously past
		// EOF, so it is unsatisfiable; any other parse error is malformed syntax
		// and is ignored. This keeps a hostile "bytes=999...999-" as a clean 416
		// rather than a 200 that quietly serves the whole object.
		if errors.Is(err, strconv.ErrRange) {
			return rangeUnsatisfiable
		}
		return rangeIgnore
	}
	if start < 0 {
		return rangeIgnore
	}
	if start >= size {
		return rangeUnsatisfiable
	}
	if endStr == "" {
		return rangeSatisfiable
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return rangeIgnore
	}
	return rangeSatisfiable
}

// DeleteObjectHandler deletes an object (file) from the specified bucket.
func DeleteObjectHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := filepath.Clean(c.Param("objectKey"))
	if err := removeObject(bucketName, objectKey); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error deleting object")
		return
	}
	c.Status(http.StatusNoContent)
}

// removeObject deletes one object plus its metadata sidecar and collapses the
// now-empty parent directories, keeping the per-bucket byte gauge symmetric
// with UploadObjectHandler's Add. objectKey must already be cleaned/validated.
// A missing object is not an error (delete is idempotent); only a real removal
// failure is returned. Shared by single DeleteObject and batch DeleteObjects so
// the gauge/sidecar/dir-collapse contract lives in exactly one place.
func removeObject(bucketName, objectKey string) error {
	filePath := filepath.Join(objectsRoot, bucketName, objectKey)
	// Same stripe lock as finalizeObjectWrite so a delete cannot land between a
	// concurrent write's object rename and its sidecar write.
	defer lockObjectPath(filePath)()

	// Capture size before removal so the gauge can be decremented. A Stat
	// error (e.g. concurrent delete) is non-fatal — skip the gauge update.
	var removedBytes int64
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		removedBytes = info.Size()
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if removedBytes > 0 {
		middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Sub(float64(removedBytes))
	}

	// Best-effort metadata sidecar cleanup; a missing sidecar is not an error.
	_ = os.Remove(filePath + ".meta")

	// Collapse now-empty parent directories up to the bucket root. Stop on
	// the first non-empty dir or any error so we never remove unrelated
	// content or the bucket root itself.
	parentDir := filepath.Dir(filePath)
	bucketDir := filepath.Join(objectsRoot, bucketName)
	for parentDir != bucketDir && parentDir != "/" {
		entries, err := os.ReadDir(parentDir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(parentDir); err != nil {
			break
		}
		parentDir = filepath.Dir(parentDir)
	}
	middleware.RecordObjectDelete(bucketName)
	return nil
}

// GetObjectMetadataHandler retrieves the metadata for an object.
// For HEAD requests, metadata is emitted as response headers (S3 HeadObject
// contract); for GET requests, it is returned as a JSON body for admin use.
func GetObjectMetadataHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	objectPath := filepath.Join(objectsRoot, bucketName, objectKey)
	metadataPath := objectPath + ".meta"

	// Same nosniff guard as DownloadObjectHandler — HEAD must mirror the
	// security headers GET would emit so clients that probe with HEAD
	// before downloading do not see inconsistent guarantees.
	c.Header("X-Content-Type-Options", "nosniff")

	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Metadata not found")
		return
	}

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error opening metadata file")
		return
	}

	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error decoding metadata")
		return
	}

	// Backfill the ETag in-place so HEAD responses and the JSON body always
	// include it, even for objects predating ETag persistence.
	if tag := metadata[etagMetaKey]; tag == "" {
		backfilled, err := loadOrBackfillETag(objectPath)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError", "Error resolving ETag")
			return
		}
		metadata[etagMetaKey] = backfilled
	}

	if c.Request.Method == http.MethodHead {
		applyMetadataHeaders(c, metadata)
		// Advertise range support so a client that probes with HEAD before a
		// ranged GET sees the same Accept-Ranges the GET path emits via
		// ServeContent. S3's HeadObject carries this header for the same reason.
		c.Header("Accept-Ranges", "bytes")
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, metadata)
}
