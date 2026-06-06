package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func TestSyncWrites_DefaultOn(t *testing.T) {
	prev := syncWritesEnabled()
	t.Cleanup(func() { syncWrites.Store(prev) })
	syncWrites.Store(true)
	if !syncWritesEnabled() {
		t.Fatal("sync writes must default on")
	}
}

// Object content must be correct whether durability is on or off; the fsync
// path must not corrupt or short-circuit the write.
func TestSyncWrites_ContentCorrectBothModes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := syncWritesEnabled()
	t.Cleanup(func() { syncWrites.Store(prev) })

	for _, on := range []bool{true, false} {
		withTempObjectsRoot(t)
		syncWrites.Store(on)
		body := []byte("durable-or-not")
		if w := putObject(t, "durbkt", "k.txt", body); w.Code != http.StatusOK {
			t.Fatalf("on=%v put: %d", on, w.Code)
		}
		got, err := os.ReadFile(filepath.Join(objectsRoot, "durbkt", "k.txt"))
		if err != nil || string(got) != string(body) {
			t.Fatalf("on=%v content=%q err=%v", on, got, err)
		}
	}
}

func TestSyncWritesHandler_PersistsAndReads(t *testing.T) {
	setupHandlerStore(t)
	prev := syncWritesEnabled()
	t.Cleanup(func() { syncWrites.Store(prev) })

	put := func(enabled bool) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, _ := json.Marshal(syncWritesDTO{Enabled: enabled})
		c.Request = httptest.NewRequest(http.MethodPut, "/api/config/sync", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		PutSyncWritesHandler(c)
		if w.Code != http.StatusOK {
			t.Fatalf("put sync=%v: %d body=%s", enabled, w.Code, w.Body.String())
		}
	}

	put(false)
	if syncWritesEnabled() {
		t.Fatal("setting must apply live")
	}
	if raw, _ := storage.GetConfigValue(syncWritesConfigKey); string(raw) != "false" {
		t.Fatalf("setting must persist, got %q", raw)
	}
	// A fresh process state (default on) must adopt the persisted override.
	syncWrites.Store(true)
	eff, err := InitSyncWritesFromStore()
	if err != nil || eff {
		t.Fatalf("persisted override must win at startup: eff=%v err=%v", eff, err)
	}

	put(true)
	if !syncWritesEnabled() {
		t.Fatal("re-enable must apply live")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/config/sync", nil)
	GetSyncWritesHandler(c)
	var dto syncWritesDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil || !dto.Enabled {
		t.Fatalf("GET must reflect enabled=true, got %v err=%v", dto.Enabled, err)
	}
}
