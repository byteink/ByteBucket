package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func TestClampRetentionDays(t *testing.T) {
	cases := map[int]int{
		0:    minRetentionDays,
		-10:  minRetentionDays,
		1:    1,
		30:   30,
		365:  365,
		1000: maxRetentionDays,
	}
	for in, want := range cases {
		if got := clampRetentionDays(in); got != want {
			t.Fatalf("clamp(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRetentionHandler_PutPersistsAndClamps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	prev := requestRetentionDays.Load()
	t.Cleanup(func() { requestRetentionDays.Store(prev) })

	// An over-range value is clamped, not rejected.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/config/retention",
		strings.NewReader(`{"days":9999}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PutRetentionHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var out retentionDTO
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Days != maxRetentionDays {
		t.Fatalf("clamped days = %d, want %d", out.Days, maxRetentionDays)
	}
	if RequestRetentionDays() != maxRetentionDays {
		t.Fatalf("live retention not updated: %d", RequestRetentionDays())
	}

	// The persisted value must survive a re-init from the store.
	requestRetentionDays.Store(defaultRetentionDays)
	eff, err := InitRequestRetentionFromStore()
	if err != nil {
		t.Fatalf("init from store: %v", err)
	}
	if eff != maxRetentionDays {
		t.Fatalf("restored retention = %d, want %d", eff, maxRetentionDays)
	}
}

func TestRetentionHandler_GetReportsLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := requestRetentionDays.Load()
	t.Cleanup(func() { requestRetentionDays.Store(prev) })
	requestRetentionDays.Store(14)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/config/retention", nil)
	GetRetentionHandler(c)
	var out retentionDTO
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Days != 14 {
		t.Fatalf("get days = %d, want 14", out.Days)
	}
}
