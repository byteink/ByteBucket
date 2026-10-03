package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"
)

func TestParseBoolEnv(t *testing.T) {
	cases := map[string]bool{"true": true, "1": true, "TRUE": true, "false": false, "": false, "garbage": false}
	for in, want := range cases {
		t.Setenv("TEST_BOOL", in)
		if got := parseBoolEnv("TEST_BOOL"); got != want {
			t.Fatalf("parseBoolEnv(%q)=%v want %v", in, got, want)
		}
	}
}

func TestResolvePublicBaseURL(t *testing.T) {
	// An explicit value is used verbatim (trimmed).
	t.Setenv("PUBLIC_BASE_URL", "  https://bb.example.com  ")
	if got := resolvePublicBaseURL(); got != "https://bb.example.com" {
		t.Fatalf("explicit: got %q", got)
	}
	// Unset falls back to the localhost storage default so presign works locally.
	t.Setenv("PUBLIC_BASE_URL", "")
	if got := resolvePublicBaseURL(); got != defaultPublicBaseURL {
		t.Fatalf("unset: got %q want %q", got, defaultPublicBaseURL)
	}
}

func TestParseBoolEnvDefault(t *testing.T) {
	// Empty/malformed fall back to the supplied default (both directions).
	t.Setenv("TEST_BOOLD", "")
	if !parseBoolEnvDefault("TEST_BOOLD", true) {
		t.Fatal("empty must return default true")
	}
	t.Setenv("TEST_BOOLD", "garbage")
	if parseBoolEnvDefault("TEST_BOOLD", false) {
		t.Fatal("malformed must return default false")
	}
	// An explicit value overrides the default.
	t.Setenv("TEST_BOOLD", "false")
	if parseBoolEnvDefault("TEST_BOOLD", true) {
		t.Fatal("explicit false must override default true")
	}
	t.Setenv("TEST_BOOLD", "true")
	if !parseBoolEnvDefault("TEST_BOOLD", false) {
		t.Fatal("explicit true must override default false")
	}
}

func TestParseFloatEnv(t *testing.T) {
	cases := map[string]float64{"12.5": 12.5, "0": 0, "": 0, "abc": 0, "  3  ": 3}
	for in, want := range cases {
		t.Setenv("TEST_FLOAT", in)
		if got := parseFloatEnv("TEST_FLOAT"); got != want {
			t.Fatalf("parseFloatEnv(%q)=%v want %v", in, got, want)
		}
	}
}

func TestParseIntEnv(t *testing.T) {
	cases := map[string]int{"42": 42, "0": 0, "": 0, "1.5": 0, "abc": 0}
	for in, want := range cases {
		t.Setenv("TEST_INT", in)
		if got := parseIntEnv("TEST_INT"); got != want {
			t.Fatalf("parseIntEnv(%q)=%v want %v", in, got, want)
		}
	}
}

func TestLoadRateLimitConfig_ReadsAllFields(t *testing.T) {
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Setenv("RATE_LIMIT_RPS", "10.5")
	t.Setenv("RATE_LIMIT_BURST", "20")
	cfg := loadRateLimitConfig()
	if !cfg.Enabled || cfg.RPS != 10.5 || cfg.Burst != 20 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
}

func TestLoadIPBanConfig(t *testing.T) {
	keys := []string{"IP_BAN_ENABLED", "IP_BAN_MAX_FAILURES", "IP_BAN_WINDOW_SECONDS", "IP_BAN_SECONDS"}
	set := func(t *testing.T, vals ...string) {
		for i, k := range keys {
			t.Setenv(k, vals[i])
		}
	}
	t.Run("defaults when unset", func(t *testing.T) {
		set(t, "", "", "", "")
		if got := loadIPBanConfig(); got != middleware.DefaultIPBanConfig() {
			t.Fatalf("defaults = %+v", got)
		}
	})
	t.Run("reads all fields", func(t *testing.T) {
		set(t, "true", "5", "30", "120")
		want := middleware.IPBanConfig{Enabled: true, MaxFailures: 5, WindowSeconds: 30, BanSeconds: 120}
		if got := loadIPBanConfig(); got != want {
			t.Fatalf("cfg = %+v, want %+v", got, want)
		}
	})
	t.Run("out-of-range fields fall back to defaults", func(t *testing.T) {
		set(t, "true", "0", "7200", "abc")
		want := middleware.IPBanConfig{Enabled: true, MaxFailures: 20, WindowSeconds: 60, BanSeconds: 900}
		if got := loadIPBanConfig(); got != want {
			t.Fatalf("cfg = %+v, want %+v", got, want)
		}
	})
}

// initTestStore opens an isolated BoltDB in a temp dir, mirroring the handlers
// package fixture, so the startup override path runs for real.
func initTestStore(t *testing.T) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := storage.InitUserStore(fmt.Sprintf("users-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitUserStore: %v", err)
	}
}

// initIPBan seeds the controller from env, then lets a persisted override win.
func TestInitIPBan(t *testing.T) {
	initTestStore(t)
	t.Setenv("IP_BAN_ENABLED", "true")
	t.Setenv("IP_BAN_MAX_FAILURES", "4")
	t.Setenv("IP_BAN_WINDOW_SECONDS", "")
	t.Setenv("IP_BAN_SECONDS", "")
	ctrl, err := initIPBan()
	if err != nil {
		t.Fatalf("initIPBan: %v", err)
	}
	if got := ctrl.Current(); !got.Enabled || got.MaxFailures != 4 || got.BanSeconds != 900 {
		t.Fatalf("env seed = %+v", got)
	}

	if err := storage.PutConfigValue("ipban", []byte(`{"enabled":false,"maxFailures":9,"windowSeconds":10,"banSeconds":30}`)); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	ctrl, err = initIPBan()
	if err != nil {
		t.Fatalf("initIPBan with override: %v", err)
	}
	want := middleware.IPBanConfig{MaxFailures: 9, WindowSeconds: 10, BanSeconds: 30}
	if got := ctrl.Current(); got != want {
		t.Fatalf("override = %+v, want %+v", got, want)
	}

	restore := storage.SetConfigStoreFaultForTest(errors.New("injected"))
	defer restore()
	if _, err := initIPBan(); err == nil {
		t.Fatal("store read fault swallowed")
	}
}

// The other startup helpers share initIPBan's shape: seed from env, apply any
// persisted override, and abort startup on a store read fault.
func TestInitHelpers(t *testing.T) {
	initTestStore(t)
	if err := storage.InitEventStore(fmt.Sprintf("logs-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Setenv("RATE_LIMIT_RPS", "5")
	t.Setenv("RATE_LIMIT_BURST", "7")
	t.Setenv("TRUSTED_PROXY_HEADERS", "CF-Connecting-IP")
	t.Cleanup(func() { storage.SetTrustedProxy(storage.TrustedProxyConfig{}) })

	rl, err := initRateLimit()
	if err != nil || !rl.Current().Enabled || rl.Current().Burst != 7 {
		t.Fatalf("initRateLimit = %+v (%v)", rl, err)
	}
	if err := initTrustedProxy(); err != nil || storage.TrustedProxy().Headers[0] != "CF-Connecting-IP" {
		t.Fatalf("initTrustedProxy = %v, cfg %+v", err, storage.TrustedProxy())
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := initAccessLog(ctx); err != nil {
		t.Fatalf("initAccessLog: %v", err)
	}
	cancel()

	restore := storage.SetConfigStoreFaultForTest(errors.New("injected"))
	defer restore()
	if _, err := initRateLimit(); err == nil {
		t.Fatal("initRateLimit swallowed a store fault")
	}
	if err := initTrustedProxy(); err == nil {
		t.Fatal("initTrustedProxy swallowed a store fault")
	}
	if err := initAccessLog(context.Background()); err == nil {
		t.Fatal("initAccessLog swallowed a store fault")
	}
}

func TestLoadTrustedProxyConfig(t *testing.T) {
	t.Run("reads headers and leftmost", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXY_HEADERS", " CF-Connecting-IP , X-Forwarded-For ")
		t.Setenv("TRUSTED_PROXY_USE_LEFTMOST_IP", "true")
		t.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "")
		cfg := loadTrustedProxyConfig()
		if len(cfg.Headers) != 2 || cfg.Headers[0] != "CF-Connecting-IP" || cfg.Headers[1] != "X-Forwarded-For" || !cfg.UseLeftmostIP {
			t.Fatalf("unexpected cfg: %+v", cfg)
		}
	})
	t.Run("back-compat shim trusts XFF when only legacy proxies set", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXY_HEADERS", "")
		t.Setenv("TRUSTED_PROXY_USE_LEFTMOST_IP", "")
		t.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "2")
		cfg := loadTrustedProxyConfig()
		if len(cfg.Headers) != 1 || cfg.Headers[0] != "X-Forwarded-For" || cfg.UseLeftmostIP {
			t.Fatalf("shim cfg: %+v", cfg)
		}
	})
	t.Run("default empty", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXY_HEADERS", "")
		t.Setenv("TRUSTED_PROXY_USE_LEFTMOST_IP", "")
		t.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "")
		if cfg := loadTrustedProxyConfig(); len(cfg.Headers) != 0 || cfg.UseLeftmostIP {
			t.Fatalf("default cfg: %+v", cfg)
		}
	})
}

func TestLoadEncryptionKey(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY", "")
		if _, err := loadEncryptionKey(); err == nil {
			t.Fatal("empty key must error")
		}
	})
	t.Run("raw 32 bytes", func(t *testing.T) {
		raw := strings.Repeat("k", 32)
		t.Setenv("ENCRYPTION_KEY", raw)
		key, err := loadEncryptionKey()
		if err != nil || string(key) != raw {
			t.Fatalf("raw key: got %q err=%v", key, err)
		}
	})
	t.Run("base64 32 bytes", func(t *testing.T) {
		want := []byte(strings.Repeat("z", 32))
		t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(want))
		key, err := loadEncryptionKey()
		if err != nil || string(key) != string(want) {
			t.Fatalf("base64 key: got %q err=%v", key, err)
		}
	})
	t.Run("base64 wrong length", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
		if _, err := loadEncryptionKey(); err == nil {
			t.Fatal("decoded key not 32 bytes must error")
		}
	})
	t.Run("not base64 and not 32 bytes", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY", "!!!not-base64-and-wrong-len!!!")
		if _, err := loadEncryptionKey(); err == nil {
			t.Fatal("undecodable key must error")
		}
	})
}

func TestConfigureGinMode(t *testing.T) {
	t.Cleanup(func() { gin.SetMode(gin.TestMode) })
	// Unset must mean release: the image and every consumer deploy without
	// GIN_MODE, and gin's own default is debug.
	t.Setenv("GIN_MODE", "")
	configureGinMode()
	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("unset: got %q want %q", gin.Mode(), gin.ReleaseMode)
	}
	// An explicit value is honoured so local dev can still opt into debug.
	t.Setenv("GIN_MODE", gin.DebugMode)
	configureGinMode()
	if gin.Mode() != gin.DebugMode {
		t.Fatalf("explicit: got %q want %q", gin.Mode(), gin.DebugMode)
	}
}
