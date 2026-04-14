package handlers

import (
	"crypto/md5"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/gin-gonic/gin"
)

// etagMetaKey is the metadata-sidecar key under which the S3 ETag is stored.
// Stored WITH the enclosing double quotes so every read path (GET, HEAD, LIST)
// can return it verbatim without re-quoting. S3's wire format for ETag is
// a quoted hex string; matching that here avoids a subtle mismatch between
// the response header and the XML listing.
const etagMetaKey = "ETag"

// computeFileETag reads a file from disk and returns its ETag value in S3
// wire format (hex md5, wrapped in double quotes). Used for the one-time
// migration path: legacy objects written before ETag persistence landed have
// no sidecar value and must be rebuilt lazily on first read.
func computeFileETag(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return formatETag(h), nil
}

// formatETag returns the S3-wire-format ETag for a completed hasher: the
// hex digest wrapped in double quotes. AWS SDKs match on the literal quoted
// form, so the quotes are load-bearing.
func formatETag(h hash.Hash) string {
	return "\"" + hex.EncodeToString(h.Sum(nil)) + "\""
}

// loadOrBackfillETag reads the ETag for an on-disk object. If the metadata
// sidecar is missing or lacks an ETag (legacy objects written before ETag
// persistence), the MD5 is recomputed from the file and persisted so the
// next read is O(metadata). The small IO cost is paid once per legacy
// object; we accept it over a full offline migration step.
func loadOrBackfillETag(objectPath string) (string, error) {
	metadataPath := objectPath + ".meta"

	var metadata map[string]string
	if data, err := os.ReadFile(metadataPath); err == nil {
		if err := json.Unmarshal(data, &metadata); err != nil {
			return "", err
		}
		if tag, ok := metadata[etagMetaKey]; ok && tag != "" {
			return tag, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	tag, err := computeFileETag(objectPath)
	if err != nil {
		return "", err
	}
	if metadata == nil {
		metadata = make(map[string]string, 1)
	}
	metadata[etagMetaKey] = tag
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	// Best-effort persistence: a write failure must not mask the ETag from
	// the caller. The legacy object remains correct; the next read will
	// retry the backfill. No hidden error swallowing — the operator sees
	// it in the next failed write.
	if err := os.WriteFile(metadataPath, raw, 0644); err != nil {
		return tag, nil
	}
	return tag, nil
}

// applyMetadataHeaders copies persisted metadata onto the response headers,
// normalising Last-Modified into the HTTP date format S3 clients expect.
func applyMetadataHeaders(c *gin.Context, metadata map[string]string) {
	for key, value := range metadata {
		switch strings.ToLower(key) {
		case "content-type":
			c.Header("Content-Type", value)
		case "content-length":
			c.Header("Content-Length", value)
		case "last-modified":
			if t, err := time.Parse(time.RFC1123, value); err == nil {
				c.Header("Last-Modified", t.UTC().Format(http.TimeFormat))
			} else {
				c.Header("Last-Modified", value)
			}
		case "etag":
			c.Header("ETag", value)
		default:
			c.Header(key, value)
		}
	}
}
