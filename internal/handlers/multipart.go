package handlers

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// initiateMultipartUploadResult is the S3 wire shape returned by
// CreateMultipartUpload. Element names and their order match the AWS REST API
// Reference; SDKs unmarshal via named fields but preserving source fidelity
// keeps wire captures legible when debugging against real S3.
type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// completeMultipartUploadRequestPart mirrors one <Part> element in the
// CompleteMultipartUpload request body. SDKs send ETag wire-quoted; we
// normalise on ingest.
type completeMultipartUploadRequestPart struct {
	PartNumber int    `xml:"PartNumber" json:"partNumber"`
	ETag       string `xml:"ETag" json:"etag"`
}

// completeMultipartUploadRequest is the root XML document clients POST to
// finalise an upload. JSON equivalent is accepted on the admin surface.
type completeMultipartUploadRequest struct {
	XMLName xml.Name                             `xml:"CompleteMultipartUpload" json:"-"`
	Parts   []completeMultipartUploadRequestPart `xml:"Part" json:"parts"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type listPartsResultPart struct {
	PartNumber   int    `xml:"PartNumber" json:"partNumber"`
	ETag         string `xml:"ETag" json:"etag"`
	Size         int64  `xml:"Size" json:"size"`
	LastModified string `xml:"LastModified" json:"lastModified"`
}

type listPartsResult struct {
	XMLName  xml.Name              `xml:"ListPartsResult"`
	XMLNS    string                `xml:"xmlns,attr"`
	Bucket   string                `xml:"Bucket"`
	Key      string                `xml:"Key"`
	UploadID string                `xml:"UploadId"`
	Parts    []listPartsResultPart `xml:"Part"`
}

type listMultipartUploadsEntry struct {
	Key       string `xml:"Key" json:"key"`
	UploadID  string `xml:"UploadId" json:"uploadId"`
	Initiated string `xml:"Initiated" json:"initiated"`
}

type listMultipartUploadsResult struct {
	XMLName xml.Name                    `xml:"ListMultipartUploadsResult"`
	XMLNS   string                      `xml:"xmlns,attr"`
	Bucket  string                      `xml:"Bucket"`
	Uploads []listMultipartUploadsEntry `xml:"Upload"`
}

const s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// cleanObjectKey normalises the object key captured by Gin's wildcard.
// Gin retains the leading slash (e.g. "/foo/bar") while S3 keys are stored
// and echoed back without one. Stripping here keeps the storage layer honest
// about what a key looks like and lets Complete/Abort callers compare keys
// verbatim against the manifest written at Create time.
func cleanObjectKey(c *gin.Context) string {
	return strings.TrimPrefix(filepath.Clean(c.Param("objectKey")), "/")
}

// collectUserMetadata extracts x-amz-meta-* headers from the request, exactly
// mirroring the single-PUT path so multipart-uploaded objects expose the same
// metadata shape on GET/HEAD as single-PUT ones.
func collectUserMetadata(c *gin.Context) map[string]string {
	out := map[string]string{}
	for k, v := range c.Request.Header {
		if len(v) == 0 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			out[k] = v[0]
		}
	}
	return out
}

// CreateMultipartUploadHandler handles POST /:bucket/:key?uploads. The upload
// ID is returned in InitiateMultipartUploadResult on SigV4 callers and as JSON
// on the admin surface.
func CreateMultipartUploadHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := cleanObjectKey(c)
	if bucket == "" || key == "" || key == "/" {
		respondError(c, http.StatusBadRequest, "InvalidRequest", "Bucket and key required")
		return
	}
	up, err := storage.CreateMultipartUpload(bucket, key, collectUserMetadata(c))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to initiate multipart upload")
		return
	}
	xmlBody := initiateMultipartUploadResult{
		XMLNS:    s3XMLNS,
		Bucket:   bucket,
		Key:      key,
		UploadID: up.UploadID,
	}
	jsonBody := gin.H{"bucket": bucket, "key": key, "uploadId": up.UploadID}
	respondXMLOrJSON(c, http.StatusOK, xmlBody, jsonBody)
}

// UploadPartHandler handles PUT /:bucket/:key?partNumber=N&uploadId=X. The
// per-part ETag is the plain hex MD5 (wire-quoted), identical to the single-
// PUT contract — the composite ETag only appears on Complete.
func UploadPartHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := cleanObjectKey(c)
	uploadID := c.Query("uploadId")
	partNumberStr := c.Query("partNumber")
	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid partNumber")
		return
	}
	part, err := storage.UploadPart(bucket, key, uploadID, partNumber, c.Request.Body)
	if err != nil {
		mapMultipartErr(c, err)
		return
	}
	// ETag is the load-bearing response datum for UploadPart; SDKs round-trip
	// it through their local part list to build the Complete request.
	c.Header("ETag", part.ETag)
	c.Status(http.StatusOK)
}

// CompleteMultipartUploadHandler handles POST /:bucket/:key?uploadId=X with a
// CompleteMultipartUpload body. Content-Type selects the parser; JSON is
// accepted on the admin surface only.
func CompleteMultipartUploadHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := cleanObjectKey(c)
	uploadID := c.Query("uploadId")

	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		respondError(c, http.StatusBadRequest, "IncompleteBody", "Failed to read request body")
		return
	}
	var req completeMultipartUploadRequest
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &req); err != nil {
			respondError(c, http.StatusBadRequest, "MalformedXML", "Invalid JSON body")
			return
		}
	} else {
		if err := xml.Unmarshal(raw, &req); err != nil {
			respondError(c, http.StatusBadRequest, "MalformedXML", "Invalid XML body")
			return
		}
	}
	if len(req.Parts) == 0 {
		respondError(c, http.StatusBadRequest, "InvalidPart", "No parts supplied")
		return
	}
	expected := make([]storage.UploadedPart, 0, len(req.Parts))
	for _, p := range req.Parts {
		expected = append(expected, storage.UploadedPart{PartNumber: p.PartNumber, ETag: p.ETag})
	}
	finalETag, _, err := storage.CompleteMultipartUpload(bucket, key, uploadID, expected)
	if err != nil {
		mapMultipartErr(c, err)
		return
	}
	xmlBody := completeMultipartUploadResult{
		XMLNS:    s3XMLNS,
		Location: "http://" + c.Request.Host + "/" + bucket + "/" + key,
		Bucket:   bucket,
		Key:      key,
		ETag:     finalETag,
	}
	jsonBody := gin.H{"bucket": bucket, "key": key, "etag": finalETag}
	// Emit the ETag header too; Uploader.Upload reads it from the response for
	// its final progress callback even though the body also carries it.
	c.Header("ETag", finalETag)
	respondXMLOrJSON(c, http.StatusOK, xmlBody, jsonBody)
}

// AbortMultipartUploadHandler handles DELETE /:bucket/:key?uploadId=X. S3
// returns 204 on success; a missing upload returns NoSuchUpload (404).
func AbortMultipartUploadHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := cleanObjectKey(c)
	uploadID := c.Query("uploadId")
	if err := storage.AbortMultipartUpload(bucket, key, uploadID); err != nil {
		mapMultipartErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListPartsHandler handles GET /:bucket/:key?uploadId=X. The XML body matches
// the S3 ListPartsResult shape so the SDK's multipart uploader can reconcile
// its local state against server-side truth.
func ListPartsHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	key := cleanObjectKey(c)
	uploadID := c.Query("uploadId")
	parts, err := storage.ListParts(bucket, key, uploadID)
	if err != nil {
		mapMultipartErr(c, err)
		return
	}
	xmlParts := make([]listPartsResultPart, 0, len(parts))
	jsonParts := make([]gin.H, 0, len(parts))
	for _, p := range parts {
		lm := p.LastModified.UTC().Format("2006-01-02T15:04:05.000Z")
		xmlParts = append(xmlParts, listPartsResultPart{
			PartNumber: p.PartNumber, ETag: p.ETag, Size: p.Size, LastModified: lm,
		})
		jsonParts = append(jsonParts, gin.H{
			"partNumber": p.PartNumber, "etag": p.ETag, "size": p.Size, "lastModified": lm,
		})
	}
	xmlBody := listPartsResult{
		XMLNS: s3XMLNS, Bucket: bucket, Key: key, UploadID: uploadID, Parts: xmlParts,
	}
	jsonBody := gin.H{"bucket": bucket, "key": key, "uploadId": uploadID, "parts": jsonParts}
	respondXMLOrJSON(c, http.StatusOK, xmlBody, jsonBody)
}

// ListMultipartUploadsHandler handles GET /:bucket?uploads. Dispatched from
// the existing bucket GET subresource switch.
func ListMultipartUploadsHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	ups, err := storage.ListMultipartUploads(bucket)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to list multipart uploads")
		return
	}
	xmlEntries := make([]listMultipartUploadsEntry, 0, len(ups))
	jsonEntries := make([]gin.H, 0, len(ups))
	for _, u := range ups {
		init := u.Initiated.UTC().Format("2006-01-02T15:04:05.000Z")
		xmlEntries = append(xmlEntries, listMultipartUploadsEntry{
			Key: u.Key, UploadID: u.UploadID, Initiated: init,
		})
		jsonEntries = append(jsonEntries, gin.H{
			"key": u.Key, "uploadId": u.UploadID, "initiated": init,
		})
	}
	xmlBody := listMultipartUploadsResult{XMLNS: s3XMLNS, Bucket: bucket, Uploads: xmlEntries}
	jsonBody := gin.H{"bucket": bucket, "uploads": jsonEntries}
	respondXMLOrJSON(c, http.StatusOK, xmlBody, jsonBody)
}

// mapMultipartErr maps storage sentinels to S3-compatible error codes. Any
// unknown error falls through to a 500; we do not leak internal error text
// because it flows straight into an XML body that SDKs may log.
func mapMultipartErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, storage.ErrNoSuchUpload):
		respondError(c, http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist")
	case errors.Is(err, storage.ErrInvalidPart):
		respondError(c, http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found")
	case errors.Is(err, storage.ErrInvalidPartOrder):
		respondError(c, http.StatusBadRequest, "InvalidPartOrder", "Parts must be specified in ascending PartNumber order")
	case errors.Is(err, storage.ErrInvalidPartRange):
		respondError(c, http.StatusBadRequest, "InvalidArgument", "PartNumber out of range")
	default:
		respondError(c, http.StatusInternalServerError, "InternalError", "Multipart operation failed")
	}
}
