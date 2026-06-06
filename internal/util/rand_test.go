package util

import (
	"strings"
	"testing"
)

func TestGenerateRandomString_LengthAndCharset(t *testing.T) {
	const n = 64
	s := GenerateRandomString(n, AccessKeyCharset)
	if len(s) != n {
		t.Fatalf("length: got %d want %d", len(s), n)
	}
	for _, r := range s {
		if !strings.ContainsRune(AccessKeyCharset, r) {
			t.Fatalf("char %q not in charset", r)
		}
	}
}

func TestGenerateRandomString_ZeroLength(t *testing.T) {
	if s := GenerateRandomString(0, AccessKeyCharset); s != "" {
		t.Fatalf("zero length: got %q want empty", s)
	}
}

// Two long draws colliding would indicate a broken/deterministic source. With a
// 62-char alphabet and length 32 the collision probability is negligible.
func TestGenerateRandomString_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		s := GenerateRandomString(32, SecretAccessKeyCharset)
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate output on draw %d: %q", i, s)
		}
		seen[s] = struct{}{}
	}
}

// A single-char charset forces every position to that char, proving the index
// math addresses the whole string deterministically at the boundary.
func TestGenerateRandomString_SingleCharCharset(t *testing.T) {
	if s := GenerateRandomString(8, "A"); s != "AAAAAAAA" {
		t.Fatalf("single-char charset: got %q", s)
	}
}
