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
	objectKey := c.Param("objectKey")
	objectKey = filepath.Clean(objectKey)

	// Resolve the canned ACL (if any) up-front so an invalid value rejects
	// the request before we spend IO writing the body. An empty header means
	// the object inherits the bucket ACL, so we only persist a value when the
	// client explicitly set one.
	aclHeader := c.GetHeader("x-amz-acl")
	cannedACL := ""
	if aclHeader != "" {
		normalized, err := storage.NormalizeCannedACL(aclHeader)
		if err != nil {
			respondError(c, http.StatusBadRequest, "InvalidArgument", "Unsupported x-amz-acl value")
			return
		}
		cannedACL = normalized
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating bucket directory")
		return
	}

	dstPath := filepath.Join(bucketPath, objectKey)
	parentDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating parent directories")
		return
	}

	f, err := os.Create(dstPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating file")
		return
	}
	// Close explicitly after streaming so an fsync/close failure surfaces as a
	// 500 rather than being swallowed by a deferred close. The MD5 hasher is
	// fed by the same MultiWriter as the CRC32 so we compute the S3 ETag in
	// one pass without re-reading the file from disk.
	crcHasher := crc32.NewIEEE()
	md5Hasher := md5.New()
	multiWriter := io.MultiWriter(f, crcHasher, md5Hasher)
	written, err := io.Copy(multiWriter, c.Request.Body)
	if err != nil {
		_ = f.Close()
		respondError(c, http.StatusInternalServerError, "InternalError", "Error saving file")
		return
	}
	if err := f.Close(); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error closing file")
		return
	}

	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, crcHasher.Sum32())
	checksumBase64 := base64.StdEncoding.EncodeToString(checksumBytes)
	etag := formatETag(md5Hasher)

	metadata, mErr := collectUserMetadataChecked(c)
	if mErr != nil {
		respondError(c, http.StatusBadRequest, "MetadataTooLarge",
			"x-amz-meta-* headers exceed 2 KiB total")
		return
	}
	metadata["x-amz-checksum-crc32"] = checksumBase64
	metadata[etagMetaKey] = etag
	metadata["Content-Length"] = strconv.FormatInt(written, 10)
	// Preserve the client-supplied Content-Type so GET emits a stable
	// value. Without this the response carried no Content-Type at all,
	// inviting MIME sniffing — combined with public-read that is a
	// stored-XSS vector for any HTML payload. Default to
	// application/octet-stream so SDKs that omit the header still get
	// a sane non-sniffable value paired with nosniff at GET time.
	if ct := strings.TrimSpace(c.GetHeader("Content-Type")); ct != "" {
		metadata["Content-Type"] = ct
	} else {
		metadata["Content-Type"] = "application/octet-stream"
	}
	if cannedACL != "" {
		metadata["acl"] = cannedACL
	}

	metadataPath := dstPath + ".meta"
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error encoding metadata")
		return
	}
	if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error writing metadata")
		return
	}

	// If the upload set an explicit canned ACL, audit it as if it were a
	// PutObjectAcl call. The visible transition is "default -> <canned>";
	// helpful when scoping which uploads opened up which objects.
	if cannedACL != "" {
		auditACLChange(c, "object", bucketName, objectKey, storage.ACLPrivate, cannedACL)
	}

	// Credit the new object's bytes against the bucket gauge. Best-effort
	// delta — not recomputed at startup — so the value should be treated
	// as a trendline, not an authoritative size report.
	middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Add(float64(written))

	// ETag is part of the S3 PutObject response contract; SDKs read it and
	// optionally verify against a client-side Content-MD5.
	c.Header("ETag", etag)
	c.Status(http.StatusOK)
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
	// it would emit a wrong Content-Length on partial reads.
	c.Writer.Header().Del("Content-Length")

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
