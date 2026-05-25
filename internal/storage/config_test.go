package storage

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"
)

// setupConfigStore initializes an isolated BoltDB in a temp dir, mirroring the
// auth package's fixture (chdir so getStoragePath resolves under the temp dir).
func setupConfigStore(t *testing.T) {
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
	if err := InitUserStore(fmt.Sprintf("users-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitUserStore: %v", err)
	}
}

func TestConfigValueRoundTrip(t *testing.T) {
	setupConfigStore(t)

	// Absent key returns (nil, nil) so callers distinguish "no override".
	got, err := GetConfigValue("ratelimit")
	if err != nil {
		t.Fatalf("GetConfigValue absent: %v", err)
	}
	if got != nil {
		t.Fatalf("absent key returned %q, want nil", got)
	}

	want := []byte(`{"enabled":true,"rps":50}`)
	if err := PutConfigValue("ratelimit", want); err != nil {
		t.Fatalf("PutConfigValue: %v", err)
	}
	if got, _ = GetConfigValue("ratelimit"); !bytes.Equal(got, want) {
		t.Fatalf("round-trip = %q, want %q", got, want)
	}

	// Overwrite replaces in place.
	want2 := []byte(`{"enabled":false}`)
	if err := PutConfigValue("ratelimit", want2); err != nil {
		t.Fatalf("PutConfigValue overwrite: %v", err)
	}
	if got, _ = GetConfigValue("ratelimit"); !bytes.Equal(got, want2) {
		t.Fatalf("after overwrite = %q, want %q", got, want2)
	}

	// Delete removes; a second delete of an absent key is not an error.
	if err := DeleteConfigValue("ratelimit"); err != nil {
		t.Fatalf("DeleteConfigValue: %v", err)
	}
	if got, _ = GetConfigValue("ratelimit"); got != nil {
		t.Fatalf("after delete = %q, want nil", got)
	}
	if err := DeleteConfigValue("ratelimit"); err != nil {
		t.Fatalf("DeleteConfigValue idempotent: %v", err)
	}
}

// TestGetConfigValueReturnsCopy guards the documented contract: the returned
// slice must be safe to retain and mutate after the read transaction, since a
// raw Bolt value is only valid within its txn.
func TestGetConfigValueReturnsCopy(t *testing.T) {
	setupConfigStore(t)
	if err := PutConfigValue("k", []byte("abc")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := GetConfigValue("k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got[0] = 'z'
	if again, _ := GetConfigValue("k"); string(again) != "abc" {
		t.Fatalf("stored value mutated via returned slice: %q", again)
	}
}
