package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestS3ActivityByBucket_AggregatesOpsAndBytes(t *testing.T) {
	// Unique bucket so the cumulative global registry can't bleed across tests.
	const b = "activity-agg-bkt"
	RecordObjectUpload(b, 100)
	RecordObjectUpload(b, 50)
	RecordObjectDownload(b, 200)
	RecordObjectDelete(b)

	got := S3ActivityByBucket()[b]
	if got == nil {
		t.Fatal("bucket missing from activity rollup")
	}
	if got.Uploads != 2 {
		t.Fatalf("uploads = %v, want 2", got.Uploads)
	}
	if got.Downloads != 1 {
		t.Fatalf("downloads = %v, want 1", got.Downloads)
	}
	if got.Deletes != 1 {
		t.Fatalf("deletes = %v, want 1", got.Deletes)
	}
	if got.BytesIn != 150 {
		t.Fatalf("bytesIn = %v, want 150", got.BytesIn)
	}
	if got.BytesOut != 200 {
		t.Fatalf("bytesOut = %v, want 200", got.BytesOut)
	}
}

func TestS3RequestOutcome_CountsByClassAndSkipsHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(S3RequestOutcome())
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/bad", func(c *gin.Context) { c.Status(http.StatusForbidden) })
	r.GET("/boom", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/favicon.ico", func(c *gin.Context) { c.Status(http.StatusOK) })

	before := S3RequestOutcomes()
	hit := func(p string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
	}
	hit("/ok")
	hit("/bad")
	hit("/boom")
	hit("/health")      // must be excluded from request-health accounting
	hit("/favicon.ico") // favicon probe is not bucket traffic — also excluded
	after := S3RequestOutcomes()

	if d := after.Success - before.Success; d != 1 {
		t.Fatalf("2xx delta = %v, want 1 (health + favicon excluded)", d)
	}
	if d := after.ClientError - before.ClientError; d != 1 {
		t.Fatalf("4xx delta = %v, want 1", d)
	}
	if d := after.ServerError - before.ServerError; d != 1 {
		t.Fatalf("5xx delta = %v, want 1", d)
	}
}

func TestMultipartInProgress_ReadsGauge(t *testing.T) {
	MultipartUploadsInProgress.Set(4)
	t.Cleanup(func() { MultipartUploadsInProgress.Set(0) })
	if got := MultipartInProgress(); got != 4 {
		t.Fatalf("multipart gauge = %v, want 4", got)
	}
}
