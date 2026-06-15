package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func seedDataEvent(t *testing.T, e storage.Event) {
	t.Helper()
	e.Category = storage.EventData
	if err := storage.AppendEvent(e); err != nil {
		t.Fatalf("seed data event: %v", err)
	}
}

func TestGetLogsHandler_DataCategoryNewestFirst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	storage.SetAccessLogMaxAge(0) // synthetic timestamps; isolate from the age cap
	seedDataEvent(t, storage.Event{TimeUnixNano: 1000, Op: "PutObject", Bucket: "b", Key: "a"})
	seedDataEvent(t, storage.Event{TimeUnixNano: 2000, Op: "GetObject", Bucket: "b", Key: "a"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/logs?category=data", nil)
	GetLogsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var dto struct {
		Events []struct {
			Ts       int64  `json:"ts"`
			Category string `json:"category"`
			Op       string `json:"op"`
			Key      string `json:"key"`
			Time     string `json:"time"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dto.Events) != 2 || dto.Events[0].Ts != 2000 || dto.Events[0].Op != "GetObject" {
		t.Fatalf("data logs wrong: %+v", dto.Events)
	}
	if dto.Events[0].Category != "data" || dto.Events[0].Time == "" {
		t.Fatalf("missing category/time: %+v", dto.Events[0])
	}
}

func TestGetLogsHandler_ControlCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	if err := storage.AppendEvent(storage.Event{
		TimeUnixNano: 500, Category: storage.EventControl, Actor: "admin", Op: "user.create", Target: "u1",
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/logs?category=control", nil)
	GetLogsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var dto struct {
		Events []struct {
			Op     string `json:"op"`
			Target string `json:"target"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dto.Events) != 1 || dto.Events[0].Op != "user.create" || dto.Events[0].Target != "u1" {
		t.Fatalf("control logs wrong: %+v", dto.Events)
	}
}

func TestGetLogsHandler_RejectsBadCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	for _, q := range []string{"/api/logs", "/api/logs?category=", "/api/logs?category=bogus"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, q, nil)
		GetLogsHandler(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q: status %d, want 400", q, w.Code)
		}
	}
}

func TestGetLogsHandler_BadBeforeIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/logs?category=data&before=notanumber", nil)
	GetLogsHandler(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad before = %d, want 400", w.Code)
	}
}
