package handlers

import (
	"encoding/xml"
	"strings"

	"github.com/gin-gonic/gin"
)

// S3ErrorBody is the canonical S3 error XML document. Keep the field layout
// and XML names stable: AWS SDKs parse these strings directly and surface the
// Code verbatim to user code.
type S3ErrorBody struct {
	XMLName   xml.Name `xml:"Error" json:"-"`
	Code      string   `xml:"Code" json:"code"`
	Message   string   `xml:"Message" json:"message"`
	RequestId string   `xml:"RequestId" json:"requestId"`
}

// wantsJSON reports whether the current request prefers a JSON response over
// S3-style XML. Any request on an admin-only path (/s3, /users, /cors) speaks
// the admin protocol; otherwise we honour an explicit Accept: application/json
// from browser callers. Default is XML to preserve exact SigV4 wire format.
func wantsJSON(c *gin.Context) bool {
	p := c.Request.URL.Path
	if strings.HasPrefix(p, "/s3") || strings.HasPrefix(p, "/users") || strings.HasPrefix(p, "/cors") {
		return true
	}
	return strings.Contains(c.GetHeader("Accept"), "application/json")
}

// respondError writes a protocol-correct error body for the current surface.
// SigV4 callers get S3 XML (SDKs pattern-match the Code element); admin UI
// callers get JSON. The RequestId is a fixed placeholder — real request
// tracing would require a middleware-injected ID, which is out of scope here.
func respondError(c *gin.Context, status int, code, message string) {
	body := S3ErrorBody{Code: code, Message: message, RequestId: "dummy-request-id"}
	if wantsJSON(c) {
		c.AbortWithStatusJSON(status, body)
		return
	}
	c.Header("Content-Type", "application/xml")
	c.AbortWithStatus(status)
	// AbortWithStatus already wrote the header; c.XML would try to write
	// headers again, so marshal directly.
	enc := xml.NewEncoder(c.Writer)
	_ = enc.Encode(body)
}

// respondXMLOrJSON serves success responses in either shape depending on the
// caller's surface. Used by subresource handlers (e.g. ?cors) where the admin
// UI expects JSON but the SigV4 surface must return S3-shaped XML.
func respondXMLOrJSON(c *gin.Context, status int, s3XML any, adminJSON any) {
	if wantsJSON(c) {
		c.JSON(status, adminJSON)
		return
	}
	c.XML(status, s3XML)
}
