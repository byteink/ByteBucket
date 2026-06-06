package handlers

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// maxDeleteKeys mirrors the AWS DeleteObjects limit of 1000 keys per request.
// Enforced so a single request cannot fan out into an unbounded delete loop.
const maxDeleteKeys = 1000

// deleteMaxBodyBytes bounds the inbound Delete document. 1000 keys at the S3
// per-key ceiling (1024 bytes) plus XML framing fits comfortably under 2 MiB;
// the read is bounded (LimitReader) so a hostile client cannot stream more.
const deleteMaxBodyBytes = 2 << 20

// deleteRequest is the wire shape of the <Delete> document (and the admin JSON
// equivalent). Quiet suppresses the per-object <Deleted> entries in the reply,
// returning only errors, exactly as AWS specifies.
type deleteRequest struct {
	XMLName xml.Name `xml:"Delete" json:"-"`
	Quiet   bool     `xml:"Quiet" json:"quiet"`
	Objects []struct {
		Key string `xml:"Key" json:"-"`
	} `xml:"Object" json:"-"`
	// JSONKeys is the compact admin shape: {"objects":["k1","k2"]}.
	JSONKeys []string `xml:"-" json:"objects"`
}

// deleteResult is the <DeleteResult> reply document for the SigV4 surface.
type deleteResult struct {
	XMLName xml.Name        `xml:"DeleteResult"`
	Deleted []deletedEntry  `xml:"Deleted"`
	Errors  []deleteFailure `xml:"Error"`
}

type deletedEntry struct {
	Key string `xml:"Key"`
}

type deleteFailure struct {
	Key     string `xml:"Key" json:"key"`
	Code    string `xml:"Code" json:"code"`
	Message string `xml:"Message" json:"message"`
}

// adminDeleteResult is the compact JSON shape for the admin UI: deleted keys
// are plain strings, mirroring the {"objects":[...]} request shape.
type adminDeleteResult struct {
	Deleted []string        `json:"deleted"`
	Errors  []deleteFailure `json:"errors"`
}

// DeleteObjectsHandler handles POST /:bucket?delete — the S3 batch delete.
// Each key is validated independently (the keys arrive in the body, so they
// bypass the ValidateNames URL chokepoint); an invalid key becomes a per-key
// Error and never touches the filesystem. A missing object deletes
// successfully (idempotent), matching AWS.
func DeleteObjectsHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	if bucket == "" {
		respondError(c, http.StatusBadRequest, "InvalidRequest", "Bucket required")
		return
	}

	keys, quiet, err := parseDeleteRequest(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, "MalformedXML", "Invalid delete document: "+err.Error())
		return
	}
	if len(keys) == 0 {
		respondError(c, http.StatusBadRequest, "MalformedXML", "Delete request must contain at least one object")
		return
	}
	if len(keys) > maxDeleteKeys {
		respondError(c, http.StatusBadRequest, "MalformedXML", "Delete request exceeds 1000 objects")
		return
	}

	var res deleteResult
	var admin adminDeleteResult
	for _, raw := range keys {
		clean, err := storage.ValidateObjectKey(raw)
		if err != nil {
			fail := deleteFailure{Key: raw, Code: "InvalidArgument", Message: "Invalid object key"}
			res.Errors = append(res.Errors, fail)
			admin.Errors = append(admin.Errors, fail)
			continue
		}
		if err := removeObject(bucket, clean); err != nil {
			fail := deleteFailure{Key: raw, Code: "InternalError", Message: "Error deleting object"}
			res.Errors = append(res.Errors, fail)
			admin.Errors = append(admin.Errors, fail)
			continue
		}
		if !quiet {
			res.Deleted = append(res.Deleted, deletedEntry{Key: raw})
			admin.Deleted = append(admin.Deleted, raw)
		}
	}

	respondXMLOrJSON(c, http.StatusOK, res, admin)
}

// parseDeleteRequest reads, bounds, and decodes the request body into the list
// of keys plus the quiet flag. JSON (admin) and XML (SigV4) are disambiguated
// by Content-Type, exactly like the tagging and CORS handlers.
func parseDeleteRequest(c *gin.Context) ([]string, bool, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, deleteMaxBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > deleteMaxBodyBytes {
		return nil, false, errors.New("delete document too large")
	}

	var req deleteRequest
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, false, err
		}
		return req.JSONKeys, req.Quiet, nil
	}
	// encoding/xml does not resolve external entities, so XXE/entity-expansion
	// surface here as a decode error rather than data exfiltration.
	if err := xml.Unmarshal(body, &req); err != nil {
		return nil, false, err
	}
	keys := make([]string, 0, len(req.Objects))
	for _, o := range req.Objects {
		keys = append(keys, o.Key)
	}
	return keys, req.Quiet, nil
}
