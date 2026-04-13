package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// newTestRouter wires the request-ID middleware onto a tiny router exposing
// the context-stored ID so tests can assert both header and context paths.
func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, RequestID(c))
	})
	return r
}

// The middleware must set a parseable UUID both on the response header and
// in the Gin context. Skipping either breaks observability or leaves error
// responses without a correlatable body — exactly the defect it exists to
// fix.
func TestRequestIDMiddlewareSetsHeaderAndContext(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	hdr := w.Header().Get("x-amz-request-id")
	if hdr == "" {
		t.Fatalf("expected x-amz-request-id header, got empty")
	}
	if _, err := uuid.Parse(hdr); err != nil {
		t.Fatalf("header is not a valid UUID: %q (%v)", hdr, err)
	}
	if body := w.Body.String(); body != hdr {
		t.Fatalf("context ID %q does not match header %q", body, hdr)
	}
}

// Two requests must receive distinct IDs; a constant value would defeat the
// entire point of per-request correlation.
func TestRequestIDMiddlewareGeneratesUniquePerRequest(t *testing.T) {
	r := newTestRouter()

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	id1 := w1.Header().Get("x-amz-request-id")
	id2 := w2.Header().Get("x-amz-request-id")
	if id1 == "" || id2 == "" {
		t.Fatalf("missing request IDs: %q / %q", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct request IDs, got duplicate: %q", id1)
	}
}

// RequestID must return empty rather than panic when the middleware was not
// installed. Error handlers rely on this defensive contract.
func TestRequestIDReturnsEmptyWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := RequestID(c); got != "" {
		t.Fatalf("expected empty string without middleware, got %q", got)
	}
}
