package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func putAccessLog(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/config/accesslog", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	PutAccessLogHandler(c)
	return w
}

func TestAccessLogConfig_RoundTripAndApplies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)

	w := putAccessLog(t, `{"enabled":true,"maxEvents":5,"maxAgeDays":7}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status %d", w.Code)
	}
	// Live settings applied.
	if !storage.AccessLogEnabled() || storage.AccessLogMaxEvents() != 5 ||
		storage.AccessLogMaxAge() != 7*24*time.Hour {
		t.Fatalf("live settings not applied: enabled=%v max=%d age=%v",
			storage.AccessLogEnabled(), storage.AccessLogMaxEvents(), storage.AccessLogMaxAge())
	}
	// Persisted: a fresh InitAccessLogFromStore reproduces them.
	storage.SetAccessLogEnabled(false)
	storage.SetAccessLogMaxEvents(1)
	eff, err := InitAccessLogFromStore()
	if err != nil {
		t.Fatalf("init from store: %v", err)
	}
	if !eff.Enabled || eff.MaxEvents != 5 || eff.MaxAgeDays != 7 {
		t.Fatalf("persisted override wrong: %+v", eff)
	}

	// GET reflects current.
	wg := httptest.NewRecorder()
	cg, _ := gin.CreateTestContext(wg)
	cg.Request = httptest.NewRequest(http.MethodGet, "/api/config/accesslog", nil)
	GetAccessLogHandler(cg)
	var dto accessLogDTO
	if err := json.Unmarshal(wg.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if !dto.Enabled || dto.MaxEvents != 5 || dto.MaxAgeDays != 7 {
		t.Fatalf("get config wrong: %+v", dto)
	}
}

func TestAccessLogConfig_Clamps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	w := putAccessLog(t, `{"enabled":true,"maxEvents":-5,"maxAgeDays":99999}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status %d", w.Code)
	}
	if storage.AccessLogMaxEvents() != 0 {
		t.Fatalf("negative maxEvents not clamped: %d", storage.AccessLogMaxEvents())
	}
	if got := accessLogMaxAgeDays(); got != maxAccessLogAgeDays {
		t.Fatalf("oversized maxAgeDays not clamped: %d", got)
	}
}

func TestAccessLogConfig_BadBodyIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)
	w := putAccessLog(t, `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d, want 400", w.Code)
	}
}
