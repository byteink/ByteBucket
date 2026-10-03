package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

type ipBanState struct {
	Env       ipBanDTO  `json:"env"`
	Override  *ipBanDTO `json:"override"`
	Effective ipBanDTO  `json:"effective"`
}

func ipBanEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/ipban", GetIPBanHandler)
	r.PUT("/ipban", PutIPBanHandler)
	r.DELETE("/ipban", DeleteIPBanHandler)
	return r
}

func getIPBanState(t *testing.T, r *gin.Engine) ipBanState {
	t.Helper()
	w := doReq(r, http.MethodGet, "/ipban", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d body=%s", w.Code, w.Body.String())
	}
	var s ipBanState
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	return s
}

// TestIPBanEndpoints walks the full GET/PUT/DELETE cycle against real
// persistence and a live controller, mirroring TestRateLimitEndpoints.
func TestIPBanEndpoints(t *testing.T) {
	setupHandlerStore(t)
	env := middleware.DefaultIPBanConfig()
	ctrl := middleware.NewIPBanController(env)
	SetIPBanController(ctrl, env)
	r := ipBanEngine()

	s := getIPBanState(t, r)
	if s.Override != nil {
		t.Fatalf("override = %+v, want null", s.Override)
	}
	want := ipBanDTO{Enabled: false, MaxFailures: 20, WindowSeconds: 60, BanSeconds: 900}
	if s.Env != want || s.Effective != want {
		t.Fatalf("env/effective = %+v / %+v, want %+v", s.Env, s.Effective, want)
	}

	w := doReq(r, http.MethodPut, "/ipban", `{"enabled":true,"maxFailures":5,"windowSeconds":30,"banSeconds":120}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status %d body=%s", w.Code, w.Body.String())
	}
	applied := middleware.IPBanConfig{Enabled: true, MaxFailures: 5, WindowSeconds: 30, BanSeconds: 120}
	if got := ctrl.Current(); got != applied {
		t.Fatalf("controller = %+v, want %+v", got, applied)
	}
	var put struct {
		Effective ipBanDTO `json:"effective"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &put); err != nil || put.Effective.MaxFailures != 5 {
		t.Fatalf("PUT body = %s (%v)", w.Body.String(), err)
	}
	s = getIPBanState(t, r)
	if s.Override == nil || s.Override.BanSeconds != 120 || !s.Effective.Enabled {
		t.Fatalf("override not reflected: %+v", s)
	}

	w = doReq(r, http.MethodDelete, "/ipban", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status %d", w.Code)
	}
	if got := ctrl.Current(); got != env {
		t.Fatalf("after delete Current = %+v, want env %+v", got, env)
	}
	if s = getIPBanState(t, r); s.Override != nil {
		t.Fatalf("override present after delete: %+v", s.Override)
	}
}

// Invalid input is rejected with 400 and never reaches the store or the
// controller.
func TestIPBanPutRejectsInvalid(t *testing.T) {
	setupHandlerStore(t)
	env := middleware.DefaultIPBanConfig()
	ctrl := middleware.NewIPBanController(env)
	SetIPBanController(ctrl, env)
	r := ipBanEngine()

	bodies := []string{
		`not-json`,
		`{"enabled":true,"maxFailures":0,"windowSeconds":60,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":10001,"windowSeconds":60,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":0,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":3601,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":0}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":604801}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":-1}`,
		`{"enabled":true,"maxFailures":1.5,"windowSeconds":60,"banSeconds":900}`,
		`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":99999999999999999999}`,
	}
	for _, b := range bodies {
		w := doReq(r, http.MethodPut, "/ipban", b)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400", b, w.Code)
		}
	}
	if got := ctrl.Current(); got != env {
		t.Fatalf("invalid PUT mutated controller: %+v", got)
	}
	if raw, err := storage.GetConfigValue(ipBanConfigKey); err != nil || raw != nil {
		t.Fatalf("invalid PUT persisted: %s (%v)", raw, err)
	}
}

// TestInitIPBanAppliesPersistedOverride proves a stored override survives a
// restart.
func TestInitIPBanAppliesPersistedOverride(t *testing.T) {
	setupHandlerStore(t)
	env := middleware.DefaultIPBanConfig()
	ctrl := middleware.NewIPBanController(env)
	SetIPBanController(ctrl, env)

	eff, err := InitIPBanFromStore()
	if err != nil || eff != env {
		t.Fatalf("init without override = %+v (%v), want env", eff, err)
	}

	if err := storage.PutConfigValue(ipBanConfigKey,
		[]byte(`{"enabled":true,"maxFailures":7,"windowSeconds":45,"banSeconds":300}`)); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	eff, err = InitIPBanFromStore()
	want := middleware.IPBanConfig{Enabled: true, MaxFailures: 7, WindowSeconds: 45, BanSeconds: 300}
	if err != nil || eff != want || ctrl.Current() != want {
		t.Fatalf("init = %+v (%v), controller %+v, want %+v", eff, err, ctrl.Current(), want)
	}
}

// A corrupt or out-of-range stored override is surfaced, not silently
// applied: startup fails loudly and GET reports 500.
func TestIPBanRejectsBadStoredOverride(t *testing.T) {
	for _, raw := range []string{`{broken`, `{"enabled":true,"maxFailures":0,"windowSeconds":60,"banSeconds":900}`} {
		setupHandlerStore(t)
		env := middleware.DefaultIPBanConfig()
		ctrl := middleware.NewIPBanController(env)
		SetIPBanController(ctrl, env)
		if err := storage.PutConfigValue(ipBanConfigKey, []byte(raw)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := InitIPBanFromStore(); err == nil {
			t.Fatalf("init accepted stored %s", raw)
		}
		if ctrl.Current() != env {
			t.Fatalf("bad stored override applied: %+v", ctrl.Current())
		}
		if w := doReq(ipBanEngine(), http.MethodGet, "/ipban", ""); w.Code != http.StatusInternalServerError {
			t.Fatalf("GET with bad stored %s = %d, want 500", raw, w.Code)
		}
	}
}

// putConfigJSON must surface an encode failure rather than persist nothing and
// report success.
func TestPutConfigJSONEncodeError(t *testing.T) {
	setupHandlerStore(t)
	if err := putConfigJSON("x", make(chan int)); err == nil {
		t.Fatal("unencodable value accepted")
	}
	if raw, err := storage.GetConfigValue("x"); err != nil || raw != nil {
		t.Fatalf("encode failure persisted: %s (%v)", raw, err)
	}
	if err := putConfigJSON("x", ipBanDTO{MaxFailures: 3}); err != nil {
		t.Fatalf("valid value: %v", err)
	}
}
