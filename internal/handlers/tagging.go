package handlers

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// taggingMaxBodyBytes caps an inbound Tagging document. A legal set is 10 tags
// of 128+256 chars plus XML framing — a few KiB at most. 16 KiB is a generous
// ceiling that still denies a hostile client the chance to make the server
// buffer megabytes before parsing. The read is bounded (LimitReader) so an
// attacker cannot stream an unbounded body.
const taggingMaxBodyBytes = 16 * 1024

// s3Tag is the wire shape of a single <Tag> element. Element names match the
// AWS REST API so SDKs marshal/unmarshal this structure without translation.
type s3Tag struct {
	Key   string `xml:"Key" json:"key"`
	Value string `xml:"Value" json:"value"`
}

// s3Tagging is the <Tagging> root document. It is used for both decoding an
// inbound PutObjectTagging body and encoding the GetObjectTagging response, so
// the SigV4 surface speaks exactly the AWS grammar.
type s3Tagging struct {
	XMLName xml.Name `xml:"Tagging"`
	TagSet  struct {
		Tags []s3Tag `xml:"Tag"`
	} `xml:"TagSet"`
}

// adminTagging is the compact JSON shape the admin UI exchanges. Keeping it
// separate from the XML struct avoids leaking XML field names into the admin
// protocol and matches the CORS handler's surface split.
type adminTagging struct {
	TagSet []s3Tag `json:"tagSet"`
}

// resolveObjectForTagging validates the addressed object and returns its
// on-disk path. It centralises the NoSuchKey contract so every tagging verb
// fails identically on a missing object. The key was already run through
// ValidateNames middleware (which rejects traversal and sidecar-named keys),
// so a request that reaches here cannot point at a ".tags.json" sidecar.
func resolveObjectForTagging(c *gin.Context) (string, bool) {
	bucket := c.Param("bucket")
	key := strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")
	if bucket == "" || key == "" {
		respondError(c, http.StatusBadRequest, "InvalidRequest", "Bucket and key required")
		return "", false
	}
	objectPath := filepath.Join(objectsRoot, bucket, key)
	if info, err := os.Stat(objectPath); err != nil || info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Object not found")
		return "", false
	}
	return objectPath, true
}

// parseInboundTagging reads, bounds, and decodes a PutObjectTagging body into
// the storage tag slice. JSON bodies (admin UI) and XML bodies (SigV4) are both
// accepted, disambiguated by Content-Type exactly like the CORS handler. The
// returned slice is NOT yet limit-validated — the caller does that so it can
// map the failure to InvalidTag.
func parseInboundTagging(c *gin.Context) ([]storage.ObjectTag, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, taggingMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > taggingMaxBodyBytes {
		return nil, errors.New("tagging document too large")
	}

	var wire []s3Tag
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		var j adminTagging
		if err := json.Unmarshal(body, &j); err != nil {
			return nil, err
		}
		wire = j.TagSet
	} else {
		// Go's encoding/xml does not resolve external entities and treats an
		// undefined entity reference as a parse error, so XXE and entity-
		// expansion bombs surface here as a decode failure rather than data
		// exfiltration or unbounded expansion.
		var doc s3Tagging
		if err := xml.Unmarshal(body, &doc); err != nil {
			return nil, err
		}
		wire = doc.TagSet.Tags
	}

	tags := make([]storage.ObjectTag, 0, len(wire))
	for _, t := range wire {
		tags = append(tags, storage.ObjectTag{Key: t.Key, Value: t.Value})
	}
	return tags, nil
}

// PutObjectTaggingHandler handles PUT /:bucket/:key?tagging. It replaces the
// object's full tag set. The target object must exist; tagging never creates
// it. On success S3 returns 200 with an empty body.
func PutObjectTaggingHandler(c *gin.Context) {
	objectPath, ok := resolveObjectForTagging(c)
	if !ok {
		return
	}
	tags, err := parseInboundTagging(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "MalformedXML", "Invalid tagging document: "+err.Error())
		return
	}
	if err := storage.ValidateObjectTags(tags); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidTag",
			"Tag set violates limits: max 10 tags, key 1-128 chars, value 0-256 chars, no duplicate keys")
		return
	}
	if err := storage.SetObjectTags(objectPath, tags); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist object tags")
		return
	}
	c.Status(http.StatusOK)
}

// GetObjectTaggingHandler handles GET /:bucket/:key?tagging. It returns the
// current tag set as the <Tagging> XML document on the SigV4 surface, or a
// compact JSON shape on the admin surface. An object with no tags returns an
// empty TagSet, never a 404 for the tags themselves.
func GetObjectTaggingHandler(c *gin.Context) {
	objectPath, ok := resolveObjectForTagging(c)
	if !ok {
		return
	}
	tags, err := storage.GetObjectTags(objectPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to load object tags")
		return
	}

	wire := make([]s3Tag, 0, len(tags))
	for _, t := range tags {
		wire = append(wire, s3Tag{Key: t.Key, Value: t.Value})
	}

	if wantsJSON(c) {
		c.JSON(http.StatusOK, adminTagging{TagSet: wire})
		return
	}
	var doc s3Tagging
	doc.TagSet.Tags = wire
	c.XML(http.StatusOK, doc)
}

// DeleteObjectTaggingHandler handles DELETE /:bucket/:key?tagging. It removes
// the entire tag set. The operation is idempotent: deleting tags on an object
// that has none still succeeds. On success S3 returns 204 with no body.
func DeleteObjectTaggingHandler(c *gin.Context) {
	objectPath, ok := resolveObjectForTagging(c)
	if !ok {
		return
	}
	if err := storage.DeleteObjectTags(objectPath); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to delete object tags")
		return
	}
	c.Status(http.StatusNoContent)
}
