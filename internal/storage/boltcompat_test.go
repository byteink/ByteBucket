package storage

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// On-disk format compatibility between the archived github.com/boltdb/bolt and
// the maintained go.etcd.io/bbolt fork. The fixture at fixtureRelPath is a real
// users.db written by boltdb v1.3.1 through this package's production code paths
// (see the generator that produced it). This test opens that legacy file under
// whichever bolt implementation is currently linked and asserts every bucket and
// key scheme reads back the known seeded values. It is the regression guard that
// the migration did not silently break production data, and it keeps guarding
// every future bbolt bump.

const (
	fixtureRelPath = "testdata/legacy-boltdb-users.db"

	fixtureEncKey      = "0123456789abcdef0123456789abcdef" // 32 bytes -> AES-256
	fixtureAccessKey   = "AKIALEGACYFIXTURE001"
	fixtureSecret      = "legacy-secret-value-0123456789abcdefXYZ0"
	fixtureConfigKey   = "ratelimit"
	fixtureConfigValue = `{"enabled":true,"rps":50}`

	fixtureMinuteUnix int64  = 1700000040 // minute boundary (1700000040 % 60 == 0)
	fixtureC2xx       uint32 = 17
	fixtureC4xx       uint32 = 3
	fixtureC5xx       uint32 = 1
)

// copyFile copies src to dst, failing the test on any error. Used both by the
// fixture generator and by openLegacyFixtureStore.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dst %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close dst %s: %v", dst, err)
	}
}

// openLegacyFixtureStore copies the committed legacy fixture into an isolated
// temp data dir and opens it through the real InitUserStore path, leaving the
// committed fixture untouched. The encryption key is set to the one the fixture
// was sealed with so the stored secret can be decrypted.
func openLegacyFixtureStore(t *testing.T) {
	t.Helper()
	abs, err := filepath.Abs(fixtureRelPath)
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("legacy fixture missing (%s): %v", abs, err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	copyFile(t, abs, filepath.Join(dir, "data", "legacy.db"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	SetEncryptionKey([]byte(fixtureEncKey))
	if err := InitUserStore("legacy.db"); err != nil {
		t.Fatalf("InitUserStore on legacy file: %v", err)
	}
}

func TestLegacyBoltFileOpensUnderCurrentImpl(t *testing.T) {
	openLegacyFixtureStore(t)

	// Users bucket: record reads back, ACL is the admin pattern, and the sealed
	// secret decrypts with the original key -> value bytes survived intact.
	u, err := GetUser(fixtureAccessKey)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.AccessKeyID != fixtureAccessKey {
		t.Fatalf("accessKeyID = %q, want %q", u.AccessKeyID, fixtureAccessKey)
	}
	if len(u.ACL) != 1 || u.ACL[0].Effect != "Allow" ||
		len(u.ACL[0].Buckets) != 1 || u.ACL[0].Buckets[0] != "*" ||
		len(u.ACL[0].Actions) != 1 || u.ACL[0].Actions[0] != "*" {
		t.Fatalf("ACL = %+v, want admin [*][*] Allow", u.ACL)
	}
	secret, err := Decrypt(u.EncryptedSecret)
	if err != nil {
		t.Fatalf("Decrypt sealed secret: %v", err)
	}
	if secret != fixtureSecret {
		t.Fatalf("decrypted secret = %q, want %q", secret, fixtureSecret)
	}

	// Config bucket: opaque bytes read back unchanged.
	cfg, err := GetConfigValue(fixtureConfigKey)
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if string(cfg) != fixtureConfigValue {
		t.Fatalf("config = %q, want %q", cfg, fixtureConfigValue)
	}

	// RequestSamples bucket: big-endian-keyed fixed-width record decodes.
	samples, err := QueryRequestSamples(fixtureMinuteUnix, fixtureMinuteUnix+60)
	if err != nil {
		t.Fatalf("QueryRequestSamples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(samples))
	}
	if s := samples[0]; s.MinuteUnix != fixtureMinuteUnix ||
		s.C2xx != fixtureC2xx || s.C4xx != fixtureC4xx || s.C5xx != fixtureC5xx {
		t.Fatalf("sample = %+v, want {%d %d %d %d}", s,
			fixtureMinuteUnix, fixtureC2xx, fixtureC4xx, fixtureC5xx)
	}

	// The control-plane audit trail no longer lives in users.db (it moved to
	// logs.db, see events.go), so the legacy file's AuditLog bucket is abandoned
	// by design and is not asserted here. The migration guard still covers every
	// bucket that remains in users.db: Users, Config, RequestSamples.
}
