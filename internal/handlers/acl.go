package handlers

import (
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// auditACLChange writes a structured "acl_change" log line whenever a
// caller mutates an ACL, on either the bucket or the object level. The
// fields are stable so an operator can later answer "who made this bucket
// public, when, with which credentials". One line per change is enough —
// forensics work joins on request_id to recover the full request context.
//
// Only emit when the new value differs from the prior one, so a re-apply
// of the same canned value does not flood the log.
func auditACLChange(c *gin.Context, resourceKind, bucket, key, oldACL, newACL string) {
	if oldACL == newACL {
		return
	}
	actor := ""
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok {
			actor = u.AccessKeyID
		}
	}
	slog.Info("acl_change",
		"resource", resourceKind,
		"bucket", bucket,
		"key", key,
		"from", oldACL,
		"to", newACL,
		"actor", actor,
		"request_id", middleware.RequestID(c),
		"remote_ip", middleware.ResolveClientIP(c.Request),
	)
}

// aclMaxBodyBytes caps the size of an inbound AccessControlPolicy document.
// 8 KiB is well above any legitimate canned-grant document and keeps a hostile
// client from forcing the server to buffer megabytes before parsing.
const aclMaxBodyBytes = 8 * 1024

// s3Grantee mirrors the AWS Grantee element. Only the URI form is honoured;
// CanonicalUser/Email grantees are accepted on the wire but reduce to private
// because ByteBucket has no user directory beyond its IAM-style ACLs.
type s3Grantee struct {
	XMLName xml.Name `xml:"Grantee"`
	Type    string   `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	URI     string   `xml:"URI,omitempty"`
	ID      string   `xml:"ID,omitempty"`
}

type s3Grant struct {
	Grantee    s3Grantee `xml:"Grantee"`
	Permission string    `xml:"Permission"`
}

type s3AccessControlPolicy struct {
	XMLName xml.Name `xml:"AccessControlPolicy"`
	Owner   struct {
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName,omitempty"`
	} `xml:"Owner"`
	AccessControlList struct {
		Grants []s3Grant `xml:"Grant"`
	} `xml:"AccessControlList"`
}

// s3AllUsersURI is the canonical S3 grantee URI for anonymous access. A
// FULL_CONTROL or READ grant on this URI reduces to "public-read"; everything
// else reduces to "private". We do NOT support public-read-write or
// authenticated-read by design.
const s3AllUsersURI = "http://acs.amazonaws.com/groups/global/AllUsers"

// reduceXMLToCanned collapses an AccessControlPolicy document into the
// canned ACL form ByteBucket persists. The reduction is strict: any grant
// that is not READ-to-AllUsers (or FULL_CONTROL-to-AllUsers, which is a
// superset of READ for anonymous principals) leaves the object private.
// This matches how AWS treats unknown/restricted grants at the storage layer
// while keeping our authz surface to two canned values.
func reduceXMLToCanned(p *s3AccessControlPolicy) string {
	for _, g := range p.AccessControlList.Grants {
		if g.Grantee.URI != s3AllUsersURI {
			continue
		}
		perm := strings.ToUpper(g.Permission)
		if perm == "READ" || perm == "FULL_CONTROL" {
			return storage.ACLPublicRead
		}
	}
	return storage.ACLPrivate
}

// readCannedACL extracts the canned ACL value to persist for a request. The
// header form ("x-amz-acl") wins over the body, matching S3's documented
// precedence; an empty header AND empty body returns "private".
func readCannedACL(c *gin.Context) (string, error) {
	if hdr := c.GetHeader("x-amz-acl"); hdr != "" {
		return storage.NormalizeCannedACL(hdr)
	}
	// Body parsing is only relevant on the ?acl subresource; PUT-object
	// requests without an x-amz-acl header inherit the bucket default.
	return storage.ACLPrivate, nil
}

// readCannedACLFromAclSubresource is the handler-side companion to readCannedACL
// for ?acl PUTs: an x-amz-acl header still wins, but if absent we parse the XML
// body for an AccessControlPolicy and reduce it to a canned form. JSON bodies
// (admin UI) accept {"canned":"private|public-read"} directly.
func readCannedACLFromAclSubresource(c *gin.Context) (string, error) {
	if hdr := c.GetHeader("x-amz-acl"); hdr != "" {
		return storage.NormalizeCannedACL(hdr)
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, aclMaxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > aclMaxBodyBytes {
		return "", errors.New("ACL document too large")
	}
	if len(body) == 0 {
		// No header, no body — keep current value rather than implicitly
		// resetting. Caller surfaces this as InvalidArgument.
		return "", errors.New("missing ACL")
	}
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		var j struct {
			Canned string `json:"canned"`
		}
		if err := json.Unmarshal(body, &j); err != nil {
			return "", err
		}
		return storage.NormalizeCannedACL(j.Canned)
	}
	var p s3AccessControlPolicy
	if err := xml.Unmarshal(body, &p); err != nil {
		return "", err
	}
	return reduceXMLToCanned(&p), nil
}

// renderACLResponse emits the canned ACL on the protocol matching the
// caller's surface. SigV4 clients receive the full AccessControlPolicy XML;
// admin UI callers receive a compact JSON shape.
func renderACLResponse(c *gin.Context, canned string) {
	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"canned": canned})
		return
	}
	ownerID := ""
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok {
			ownerID = u.AccessKeyID
		}
	}
	body := s3AccessControlPolicy{}
	body.Owner.ID = ownerID
	body.Owner.DisplayName = ownerID
	if storage.IsPublicRead(canned) {
		body.AccessControlList.Grants = []s3Grant{{
			Grantee:    s3Grantee{Type: "Group", URI: s3AllUsersURI},
			Permission: "READ",
		}}
	}
	c.XML(http.StatusOK, body)
}

// PutBucketACLHandler handles PUT /:bucket?acl. Bucket must exist; missing
// bucket surfaces as NoSuchBucket so a stale UI can distinguish from a 5xx.
func PutBucketACLHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	if bucket == "" {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Bucket name required")
		return
	}
	if info, err := os.Stat(filepath.Join(objectsRoot, bucket)); err != nil || !info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		return
	}
	canned, err := readCannedACLFromAclSubresource(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid ACL: "+err.Error())
		return
	}
	// Read the prior value before mutating so the audit line captures
	// the transition (private->public-read in particular is the one
	// that matters for incident response).
	prior, _ := storage.EffectiveBucketACL(bucket)
	if err := storage.PutBucketACL(bucket, &storage.BucketACL{Canned: canned}); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist bucket ACL")
		return
	}
	auditACLChange(c, "bucket", bucket, "", prior, canned)
	c.Status(http.StatusOK)
}

// GetBucketACLHandler handles GET /:bucket?acl.
func GetBucketACLHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	if info, err := os.Stat(filepath.Join(objectsRoot, bucket)); err != nil || !info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		return
	}
	canned, err := storage.EffectiveBucketACL(bucket)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to load bucket ACL")
		return
	}
	renderACLResponse(c, canned)
}

// PutObjectACLHandler handles PUT /:bucket/:key?acl.
func PutObjectACLHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")
	if bucket == "" || key == "" {
		respondError(c, http.StatusBadRequest, "InvalidRequest", "Bucket and key required")
		return
	}
	objectPath := filepath.Join(objectsRoot, bucket, key)
	if info, err := os.Stat(objectPath); err != nil || info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Object not found")
		return
	}
	canned, err := readCannedACLFromAclSubresource(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid ACL: "+err.Error())
		return
	}
	prior, _ := storage.EffectiveObjectACL(bucket, objectPath)
	if err := storage.SetObjectACL(objectPath, canned); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist object ACL")
		return
	}
	auditACLChange(c, "object", bucket, key, prior, canned)
	c.Status(http.StatusOK)
}

// GetObjectACLHandler handles GET /:bucket/:key?acl.
func GetObjectACLHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")
	objectPath := filepath.Join(objectsRoot, bucket, key)
	if info, err := os.Stat(objectPath); err != nil || info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchKey", "Object not found")
		return
	}
	canned, err := storage.EffectiveObjectACL(bucket, objectPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to load object ACL")
		return
	}
	renderACLResponse(c, canned)
}
