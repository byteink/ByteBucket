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
	t.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "2")
	cfg := loadRateLimitConfig()
	if !cfg.Enabled || cfg.RPS != 10.5 || cfg.Burst != 20 || cfg.TrustedProxies != 2 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
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
