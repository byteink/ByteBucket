package middleware

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
)

// entityTooLargeMessage is the S3-native text surfaced alongside the
// EntityTooLarge code. SDKs pattern-match the code; the message is informative
// for humans reading logs or the admin UI.
const entityTooLargeMessage = "Your proposed upload exceeds the maximum allowed size"

// s3ErrorBody mirrors internal/handlers.S3ErrorBody. Duplicated here rather
// than imported to avoid a middleware -> handlers import cycle; the shape is
// load-bearing S3 wire format and changes only when AWS changes it, so the
// drift risk is acceptable.
type s3ErrorBody struct {
	XMLName   xml.Name `xml:"Error" json:"-"`
	Code      string   `xml:"Code" json:"code"`
	Message   string   `xml:"Message" json:"message"`
	RequestId string   `xml:"RequestId" json:"requestId"`
}

// BodyLimit wraps next with a request-body size cap and returns a
// protocol-correct 413 EntityTooLarge the moment a downstream handler reads
// past maxBytes. Implemented at the net/http layer rather than as Gin
// middleware so it can be applied from cmd/ without touching router
// constructors — Gin's engine.Use is a no-op for routes already registered.
//
// http.MaxBytesReader would normally surface the breach as an opaque Read
// error that handlers translate to a generic 500; intercepting it here
// preserves the S3 EntityTooLarge contract on both surfaces.
//
// The limit applies to wire bytes, not decoded bytes — an attacker streaming
// a gzip-compressed body still cannot exceed maxBytes of network traffic,
// which is the correct bound for a disk-fill defence.
func BodyLimit(next http.Handler, maxBytes int64) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		// Swap the response writer for one that can be silenced after we
		// emit the 413 ourselves. Handlers that discover the Read error
		// will still call their own error helpers; without this, their
		// writes would append to our 413 body and break the response.
		sw := &silencableWriter{ResponseWriter: w}
		lb := &limitedBody{
			ReadCloser: http.MaxBytesReader(sw, r.Body, maxBytes),
			req:        r,
			sw:         sw,
		}
		r.Body = lb
		next.ServeHTTP(sw, r)
	})
}

// limitedBody forwards reads to a MaxBytesReader. The first time the reader
// reports an over-limit error it writes the 413 response and silences the
// underlying writer so the handler's follow-up error body cannot corrupt
// what we just sent.
type limitedBody struct {
	io.ReadCloser
	req       *http.Request
	sw        *silencableWriter
	triggered bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == nil || b.triggered {
		return n, err
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		b.triggered = true
		writeEntityTooLarge(b.sw, b.req)
	}
	return n, err
}

// silencableWriter wraps a ResponseWriter so BodyLimit can commit the 413
// first and then suppress further writes from the handler's own error path.
// Once silenced, WriteHeader and Write report success but drop bytes, leaving
// our 413 body intact on the wire.
type silencableWriter struct {
	http.ResponseWriter
	silenced bool
}

func (w *silencableWriter) WriteHeader(code int) {
	if w.silenced {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *silencableWriter) Write(p []byte) (int, error) {
	if w.silenced {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

// writeEntityTooLarge emits the 413 body in the format the caller's surface
// expects, then silences the writer so handler follow-ups cannot append to
// it. Admin paths and explicit JSON Accept get JSON; all other callers get
// S3 XML so AWS SDKs can surface the Code verbatim to user code.
//
// RequestId is read from the X-Amz-Request-Id header that RequestIDMiddleware
// set on the response earlier in the chain; pulling it from the response
// rather than the Gin context keeps this middleware Gin-agnostic.
func writeEntityTooLarge(sw *silencableWriter, r *http.Request) {
	body := s3ErrorBody{
		Code:      "EntityTooLarge",
		Message:   entityTooLargeMessage,
		RequestId: sw.Header().Get(requestIDHeader),
	}
	if prefersJSON(r) {
		sw.Header().Set("Content-Type", "application/json")
		sw.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(sw.ResponseWriter).Encode(body)
	} else {
		sw.Header().Set("Content-Type", "application/xml")
		sw.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = xml.NewEncoder(sw.ResponseWriter).Encode(body)
	}
	sw.silenced = true
}

// prefersJSON mirrors handlers.wantsJSON. Kept local to avoid the import
// cycle; the rules are small and stable.
func prefersJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}
