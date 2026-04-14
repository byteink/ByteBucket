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

// s3CORSRule is the wire shape of a single CORSRule in S3's XML grammar.
// Field order and element names mirror the AWS REST API Reference; AWS SDKs
// marshal this structure as-is, so any drift would break SDK interop.
type s3CORSRule struct {
	XMLName        xml.Name `xml:"CORSRule"`
	ID             string   `xml:"ID,omitempty"`
	AllowedMethod  []string `xml:"AllowedMethod"`
	AllowedOrigin  []string `xml:"AllowedOrigin"`
	AllowedHeader  []string `xml:"AllowedHeader,omitempty"`
	ExposeHeader   []string `xml:"ExposeHeader,omitempty"`
	MaxAgeSeconds  int      `xml:"MaxAgeSeconds,omitempty"`
}

// s3CORSConfiguration is the XML root for bucket CORS.
type s3CORSConfiguration struct {
	XMLName   xml.Name     `xml:"CORSConfiguration"`
	CORSRules []s3CORSRule `xml:"CORSRule"`
}

func fromStorage(cfg *storage.BucketCORSConfig) s3CORSConfiguration {
	out := s3CORSConfiguration{CORSRules: make([]s3CORSRule, 0, len(cfg.CORSRules))}
	for _, r := range cfg.CORSRules {
		out.CORSRules = append(out.CORSRules, s3CORSRule{
			ID:            r.ID,
			AllowedMethod: r.AllowedMethods,
			AllowedOrigin: r.AllowedOrigins,
			AllowedHeader: r.AllowedHeaders,
			ExposeHeader:  r.ExposeHeaders,
			MaxAgeSeconds: r.MaxAgeSeconds,
		})
	}
	return out
}

func toStorage(xmlCfg s3CORSConfiguration) storage.BucketCORSConfig {
	rules := make([]storage.BucketCORSRule, 0, len(xmlCfg.CORSRules))
	for _, r := range xmlCfg.CORSRules {
		rules = append(rules, storage.BucketCORSRule{
			ID:             r.ID,
			AllowedMethods: r.AllowedMethod,
			AllowedOrigins: r.AllowedOrigin,
			AllowedHeaders: r.AllowedHeader,
			ExposeHeaders:  r.ExposeHeader,
			MaxAgeSeconds:  r.MaxAgeSeconds,
		})
	}
	return storage.BucketCORSConfig{CORSRules: rules}
}

// PutBucketCORSHandler handles PUT /:bucket?cors. SigV4 callers send the
// S3 XML document; the admin UI sends JSON with storage-layer field names.
// The Content-Type header disambiguates — we do not rely on path because the
// admin UI may (future) present an S3 XML import flow.
func PutBucketCORSHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	if bucket == "" {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Bucket name required")
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		respondError(c, http.StatusBadRequest, "IncompleteBody", "Failed to read request body")
		return
	}

	var cfg storage.BucketCORSConfig
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, &cfg); err != nil {
			respondError(c, http.StatusBadRequest, "MalformedCORSConfiguration", "Invalid JSON body")
			return
		}
	} else {
		var xmlCfg s3CORSConfiguration
		if err := xml.Unmarshal(body, &xmlCfg); err != nil {
			respondError(c, http.StatusBadRequest, "MalformedCORSConfiguration", "Invalid XML body")
			return
		}
		cfg = toStorage(xmlCfg)
	}

	if err := storage.PutBucketCORS(bucket, &cfg); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist CORS configuration")
		return
	}
	// S3 returns 200 with an empty body on successful PutBucketCors.
	c.Status(http.StatusOK)
}

// GetBucketCORSHandler handles GET /:bucket?cors, returning XML on the SigV4
// surface and JSON on the admin surface.
func GetBucketCORSHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	cfg, err := storage.GetBucketCORS(bucket)
	if err != nil {
		if errors.Is(err, storage.ErrNoSuchCORSConfiguration) {
			respondError(c, http.StatusNotFound, "NoSuchCORSConfiguration",
				"The CORS configuration does not exist")
			return
		}
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to load CORS configuration")
		return
	}

	respondXMLOrJSON(c, http.StatusOK, fromStorage(cfg), cfg)
}

// DeleteBucketCORSHandler handles DELETE /:bucket?cors. S3 returns 204 on
// success; we return 404 when no config exists so a stale admin UI can
// distinguish "already gone" from "never existed".
func DeleteBucketCORSHandler(c *gin.Context) {
	bucket := c.Param("bucket")
	if err := storage.DeleteBucketCORS(bucket); err != nil {
		if errors.Is(err, storage.ErrNoSuchCORSConfiguration) {
			respondError(c, http.StatusNotFound, "NoSuchCORSConfiguration",
				"The CORS configuration does not exist")
			return
		}
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to delete CORS configuration")
		return
	}
	c.Status(http.StatusNoContent)
}
