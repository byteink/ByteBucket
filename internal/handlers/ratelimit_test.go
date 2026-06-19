package handlers

import (
	"ByteBucket/internal/middleware"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

func TestValidateRateLimit(t *testing.T) {
	cases := []struct {
		name string
		in   rateLimitDTO
		ok   bool
	}{
		{"disabled zeros ok", rateLimitDTO{Enabled: false}, true},
		{"enabled valid", rateLimitDTO{Enabled: true, RPS: 10, Burst: 20}, true},
		{"negative rps", rateLimitDTO{RPS: -1}, false},
		{"rps too large", rateLimitDTO{RPS: maxRateLimitRPS + 1}, false},
		{"nan rps", rateLimitDTO{RPS: math.NaN()}, false},
		{"inf rps", rateLimitDTO{RPS: math.Inf(1)}, false},
		{"negative burst", rateLimitDTO{Burst: -1}, false},
		{"burst too large", rateLimitDTO{Burst: maxRateLimitBurst + 1}, false},
		{"enabled zero rps", rateLimitDTO{Enabled: true, RPS: 0, Burst: 5}, false},
		{"enabled zero burst", rateLimitDTO{Enabled: true, RPS: 5, Burst: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateRateLimit(tc.in)
			if tc.ok && msg != "" {
				t.Fatalf("want valid, got error %q", msg)
			}
			if !tc.ok && msg == "" {
				t.Fatalf("want rejection, got valid")
			}
		})
	}
}

// setupHandlerStore initializes an isolated BoltDB so the persistence path runs
// for real, mirroring the auth package fixture.
func setupHandlerStore(t *testing.T) {
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
	if err := storage.InitUserStore(fmt.Sprintf("users-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitUserStore: %v", err)
	}
	if err := storage.InitEventStore(fmt.Sprintf("logs-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
}

func doReq(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRateLimitEndpoints exercises the full GET/PUT/DELETE cycle against the
// real persistence and a live controller: PUT persists and applies, GET
// reflects env vs override vs effective, an invalid PUT is rejected without
// mutating the controller, and DELETE reverts to the environment baseline.
func TestRateLimitEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)

	env := middleware.RateLimitConfig{Enabled: false, RPS: 10, Burst: 20}
	ctrl := middleware.NewRateLimitController(env)
	SetRateLimitController(ctrl, env)

	r := gin.New()
	r.GET("/rl", GetRateLimitHandler)
	r.PUT("/rl", PutRateLimitHandler)
	r.DELETE("/rl", DeleteRateLimitHandler)

	type stateResp struct {
		Env       rateLimitDTO  `json:"env"`
		Override  *rateLimitDTO `json:"override"`
		Effective rateLimitDTO  `json:"effective"`
	}

	// No override: env == effective, override null.
	w := doReq(r, http.MethodGet, "/rl", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	var s stateResp
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if s.Override != nil {
		t.Fatalf("override = %+v, want null", s.Override)
	}
	if s.Effective.RPS != 10 || s.Effective.Enabled {
		t.Fatalf("effective = %+v, want env", s.Effective)
	}

	// Valid PUT persists and applies live.
	w = doReq(r, http.MethodPut, "/rl", `{"enabled":true,"rps":5,"burst":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status %d body=%s", w.Code, w.Body.String())
	}
	if got := ctrl.Current(); !got.Enabled || got.RPS != 5 || got.Burst != 3 {
		t.Fatalf("controller not applied: %+v", got)
	}

	// GET reflects the override and effective.
	w = doReq(r, http.MethodGet, "/rl", "")
	s = stateResp{}
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if s.Override == nil || s.Override.RPS != 5 {
		t.Fatalf("override not reflected: %+v", s.Override)
	}
	if s.Effective.Burst != 3 || !s.Effective.Enabled {
		t.Fatalf("effective not reflected: %+v", s.Effective)
	}

	// Invalid PUT is rejected and must not mutate the controller.
	w = doReq(r, http.MethodPut, "/rl", `{"enabled":true,"rps":-1,"burst":3}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT status %d, want 400", w.Code)
	}
	if got := ctrl.Current(); got.RPS != 5 {
		t.Fatalf("controller mutated by invalid PUT: %+v", got)
	}

	// DELETE reverts to env and clears the override.
	w = doReq(r, http.MethodDelete, "/rl", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status %d", w.Code)
	}
	if got := ctrl.Current(); got != env {
		t.Fatalf("after delete Current = %+v, want env %+v", got, env)
	}
	w = doReq(r, http.MethodGet, "/rl", "")
	s = stateResp{}
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if s.Override != nil {
		t.Fatalf("override present after delete: %+v", s.Override)
	}
}

// TestInitRateLimitAppliesPersistedOverride proves a persisted override is
// applied to the live controller at startup, so a runtime setting survives a
// restart.
func TestInitRateLimitAppliesPersistedOverride(t *testing.T) {
	setupHandlerStore(t)
	base := middleware.RateLimitConfig{Enabled: false, RPS: 1, Burst: 1}
	ctrl := middleware.NewRateLimitController(base)
	SetRateLimitController(ctrl, base)

	if err := storage.PutConfigValue(rateLimitConfigKey, []byte(`{"enabled":true,"rps":9,"burst":4}`)); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	eff, err := InitRateLimitFromStore()
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !eff.Enabled || eff.RPS != 9 || eff.Burst != 4 {
		t.Fatalf("init applied = %+v, want persisted override", eff)
	}
	if got := ctrl.Current(); !got.Enabled || got.RPS != 9 || got.Burst != 4 {
		t.Fatalf("controller not updated by init: %+v", got)
	}
}
