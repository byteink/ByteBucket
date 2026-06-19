package main

import (
	"encoding/base64"
	"strings"
	"testing"
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
