package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func TestRecordAudit_StoresWithActorFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("user", &storage.User{AccessKeyID: "AKADMIN"})
	recordAudit(c, "user.create", "u1", "via test")

	got, err := storage.QueryEvents(storage.EventControl, 10, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]
	if e.Actor != "AKADMIN" || e.Op != "user.create" || e.Target != "u1" || e.Detail != "via test" {
		t.Fatalf("recorded event wrong: %+v", e)
	}
	if e.TimeUnixNano == 0 {
		t.Fatalf("event missing timestamp")
	}
}

func TestRecordAudit_NoUserLeavesActorEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	recordAudit(c, "config.sync", "false", "")
	got, _ := storage.QueryEvents(storage.EventControl, 10, 0)
	if len(got) != 1 || got[0].Actor != "" {
		t.Fatalf("expected one event with empty actor, got %+v", got)
	}
}

func TestAuditLimit_DefaultAndClamp(t *testing.T) {
	cases := map[string]int{
		"":       defaultAuditLimit,
		"0":      defaultAuditLimit,
		"-5":     defaultAuditLimit,
		"abc":    defaultAuditLimit,
		"10":     10,
		"999999": maxAuditLimit,
	}
	for in, want := range cases {
		if got := auditLimit(in); got != want {
			t.Fatalf("auditLimit(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestGetAuditHandler_NewestFirstWithLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	for i := int64(1); i <= 5; i++ {
		if err := storage.AppendEvent(storage.Event{
			TimeUnixNano: i * 1000, Category: storage.EventControl, Actor: "a", Op: "act", Target: "t",
		}); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit?limit=2", nil)
	GetAuditHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var dto struct {
		Events []struct {
			Ts     int64  `json:"ts"`
			Time   string `json:"time"`
			Action string `json:"action"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dto.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(dto.Events))
	}
	if dto.Events[0].Ts != 5000 || dto.Events[1].Ts != 4000 {
		t.Fatalf("not newest-first: %+v", dto.Events)
	}
	if dto.Events[0].Time == "" {
		t.Fatalf("event missing formatted time")
	}
}

func TestGetAuditHandler_BadBeforeIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit?before=notanumber", nil)
	GetAuditHandler(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad before = %d, want 400", w.Code)
	}
}
