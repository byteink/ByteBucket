package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func TestStats_CountsObjectsAndBytesExcludingSidecars(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)

	// Two buckets, three objects, known sizes. Tagging/ACL add sidecars that
	// must NOT be counted as objects.
	seedObject(t, "b1", "a.txt", []byte("12345"))      // 5 bytes
	seedObject(t, "b1", "nested/b.txt", []byte("678"))  // 3 bytes
	seedObject(t, "b2", "c.txt", []byte("90"))          // 2 bytes

	perBucket, objects, err := computeStorageStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(perBucket) != 2 {
		t.Fatalf("buckets=%d want 2", len(perBucket))
	}
	if objects != 3 {
		t.Fatalf("objects=%d want 3 (sidecars excluded)", objects)
	}
	var bytes int64
	for _, b := range perBucket {
		bytes += b.Bytes
	}
	if bytes != 10 {
		t.Fatalf("bytes=%d want 10", bytes)
	}
}

func TestStats_EmptyStoreIsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	perBucket, objects, err := computeStorageStats()
	if err != nil || len(perBucket) != 0 || objects != 0 {
		t.Fatalf("empty store: %d buckets / %d objects err=%v", len(perBucket), objects, err)
	}
}

func TestStatsHandler_JSONShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	seedObject(t, "b1", "a.txt", []byte("hello"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	GetStatsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var dto statsDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Buckets != 1 || dto.Objects != 1 || dto.Bytes != 5 {
		t.Fatalf("unexpected stats: %+v", dto)
	}
}

func TestStatsHandler_PerBucketSortedBySizeWithActivity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	// seedObject uploads via the real handler, so each seed records one upload
	// for its bucket — exactly the per-bucket activity the dashboard surfaces.
	seedObject(t, "act-small", "a", []byte("x"))          // 1 byte
	seedObject(t, "act-big", "a", []byte("xxxxxxxxxx"))   // 10 bytes
	seedObject(t, "act-medium", "a", []byte("xxxxx"))     // 5 bytes

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	GetStatsHandler(c)

	var dto statsDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Buckets != 3 || len(dto.PerBucket) != 3 {
		t.Fatalf("bucket count=%d perBucket=%d want 3", dto.Buckets, len(dto.PerBucket))
	}
	if dto.PerBucket[0].Name != "act-big" || dto.PerBucket[2].Name != "act-small" {
		t.Fatalf("perBucket not sorted by size desc: %+v", dto.PerBucket)
	}
	// The largest bucket's single seed must show as one recorded upload.
	if dto.PerBucket[0].Uploads < 1 {
		t.Fatalf("expected per-bucket upload activity, got %+v", dto.PerBucket[0])
	}
	if dto.Activity.Uploads < 3 {
		t.Fatalf("expected total uploads >= 3, got %v", dto.Activity.Uploads)
	}
}
