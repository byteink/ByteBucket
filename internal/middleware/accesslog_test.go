package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// setupEventStoreMW opens an isolated logs.db and enables data-plane capture.
func setupEventStoreMW(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := storage.InitEventStore(fmt.Sprintf("logs-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	storage.SetAccessLogEnabled(true)
	storage.SetAccessLogMaxAge(0) // tests run far from "now"; isolate from the age cap
}

// drainEvents flushes the async buffer so enqueued events are queryable.
func drainEvents() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storage.RunEventFlusher(ctx, time.Hour, 1000)
}

func TestS3Operation_Classification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, method, bucket, objectKey, query, copySource, want string
	}{
		{"list buckets", http.MethodGet, "", "", "", "", "ListBuckets"},
		{"list objects", http.MethodGet, "b", "", "", "", "ListObjects"},
		{"get object", http.MethodGet, "b", "/o.txt", "", "", "GetObject"},
		{"get acl", http.MethodGet, "b", "/o.txt", "acl=", "", "GetAcl"},
		{"get tagging", http.MethodGet, "b", "/o.txt", "tagging=", "", "GetObjectTagging"},
		{"presign", http.MethodGet, "b", "/o.txt", "presign=", "", "PresignObject"},
		{"list parts", http.MethodGet, "b", "/o.txt", "uploadId=u1", "", "ListParts"},
		{"put object", http.MethodPut, "b", "/o.txt", "", "", "PutObject"},
		{"create bucket", http.MethodPut, "b", "", "", "", "CreateBucket"},
		{"copy object", http.MethodPut, "b", "/o.txt", "", "src/k", "CopyObject"},
		{"upload part", http.MethodPut, "b", "/o.txt", "uploadId=u1&partNumber=1", "", "UploadPart"},
		{"put tagging", http.MethodPut, "b", "/o.txt", "tagging=", "", "PutObjectTagging"},
		{"delete object", http.MethodDelete, "b", "/o.txt", "", "", "DeleteObject"},
		{"delete bucket", http.MethodDelete, "b", "", "", "", "DeleteBucket"},
		{"delete objects", http.MethodPost, "b", "", "delete=", "", "DeleteObjects"},
		{"abort multipart", http.MethodDelete, "b", "/o.txt", "uploadId=u1", "", "AbortMultipartUpload"},
		{"create multipart", http.MethodPost, "b", "/o.txt", "uploads=", "", "CreateMultipartUpload"},
		{"complete multipart", http.MethodPost, "b", "/o.txt", "uploadId=u1", "", "CompleteMultipartUpload"},
		{"head object", http.MethodHead, "b", "/o.txt", "", "", "HeadObject"},
		{"head bucket", http.MethodHead, "b", "", "", "", "HeadBucket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			target := "/x"
			if tc.query != "" {
				target += "?" + tc.query
			}
			c.Request = httptest.NewRequest(tc.method, target, nil)
			if tc.copySource != "" {
				c.Request.Header.Set("x-amz-copy-source", tc.copySource)
			}
			c.Params = gin.Params{
				{Key: "bucket", Value: tc.bucket},
				{Key: "objectKey", Value: tc.objectKey},
			}
			if got := s3Operation(c); got != tc.want {
				t.Fatalf("s3Operation(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestAccessLog_CapturesDataEvent(t *testing.T) {
	setupEventStoreMW(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &storage.User{AccessKeyID: "AKME"}) })
	r.Use(AccessLog())
	r.GET("/:bucket/*objectKey", func(c *gin.Context) { c.String(http.StatusOK, "hello") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/photos/2026/cat.jpg", nil))
	drainEvents()

	got, _ := storage.QueryEvents(storage.EventData, 10, 0)
	if len(got) != 1 {
		t.Fatalf("got %d data events, want 1", len(got))
	}
	e := got[0]
	if e.Op != "GetObject" || e.Bucket != "photos" || e.Key != "2026/cat.jpg" {
		t.Fatalf("event identity wrong: %+v", e)
	}
	if e.Actor != "AKME" || e.Status != http.StatusOK || e.BytesOut != 5 {
		t.Fatalf("event envelope wrong: %+v", e)
	}
}

func TestAccessLog_AnonymousActor(t *testing.T) {
	setupEventStoreMW(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AccessLog())
	r.GET("/:bucket/*objectKey", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub/file.txt", nil))
	drainEvents()

	got, _ := storage.QueryEvents(storage.EventData, 10, 0)
	if len(got) != 1 || got[0].Actor != "anonymous" {
		t.Fatalf("expected one anonymous event, got %+v", got)
	}
}

func TestAccessLog_RecordsErrorCode(t *testing.T) {
	setupEventStoreMW(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AccessLog())
	r.GET("/:bucket/*objectKey", func(c *gin.Context) {
		c.Set(ErrorCodeContextKey, "NoSuchKey")
		c.Status(http.StatusNotFound)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b/missing.txt", nil))
	drainEvents()

	got, _ := storage.QueryEvents(storage.EventData, 10, 0)
	if len(got) != 1 || got[0].ErrorCode != "NoSuchKey" || got[0].Status != http.StatusNotFound {
		t.Fatalf("expected one 404 NoSuchKey event, got %+v", got)
	}
}

func TestAccessLog_DisabledRecordsNothing(t *testing.T) {
	setupEventStoreMW(t)
	storage.SetAccessLogEnabled(false)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AccessLog())
	r.GET("/:bucket/*objectKey", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b/o.txt", nil))
	drainEvents()

	got, _ := storage.QueryEvents(storage.EventData, 10, 0)
	if len(got) != 0 {
		t.Fatalf("disabled capture recorded %d events, want 0", len(got))
	}
}

func TestAccessLog_SkipsHealthFaviconAndUnmatched(t *testing.T) {
	setupEventStoreMW(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AccessLog())
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/favicon.ico", func(c *gin.Context) { c.Status(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	// Unmatched route (no registered handler) -> 404, FullPath == "".
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	drainEvents()

	got, _ := storage.QueryEvents(storage.EventData, 10, 0)
	if len(got) != 0 {
		t.Fatalf("health/favicon/unmatched recorded %d events, want 0", len(got))
	}
}
