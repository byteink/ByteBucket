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

	buckets, objects, bytes, err := computeStorageStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if buckets != 2 {
		t.Fatalf("buckets=%d want 2", buckets)
	}
	if objects != 3 {
		t.Fatalf("objects=%d want 3 (sidecars excluded)", objects)
	}
	if bytes != 10 {
		t.Fatalf("bytes=%d want 10", bytes)
	}
}

func TestStats_EmptyStoreIsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withTempObjectsRoot(t)
	buckets, objects, bytes, err := computeStorageStats()
	if err != nil || buckets != 0 || objects != 0 || bytes != 0 {
		t.Fatalf("empty store: %d/%d/%d err=%v", buckets, objects, bytes, err)
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
