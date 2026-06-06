package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestE2E_SyncWritesToggle drives the durability config endpoint against the
// running container: the setting round-trips and uploads keep working in both
// modes. Default is on; the test restores it.
func TestE2E_SyncWritesToggle(t *testing.T) {
	read := func() bool {
		resp := adminDo(t, adminRequest(t, http.MethodGet, "/api/config/sync", nil, ""))
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET sync: %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var dto struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(body, &dto); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return dto.Enabled
	}
	set := func(enabled bool) {
		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/config/sync", body, "application/json"))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT sync=%v: %d", enabled, resp.StatusCode)
		}
	}

	if !read() {
		t.Fatal("durability must default on")
	}
	t.Cleanup(func() { set(true) })

	// Disable, confirm, and confirm an upload still succeeds in non-durable mode.
	set(false)
	if read() {
		t.Fatal("setting did not persist as off")
	}
	resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/durtoggle", nil, ""))
	_ = resp.Body.Close()
	resp = adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/durtoggle/o.txt", []byte("x"), "text/plain"))
	code := resp.StatusCode
	_ = resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("upload with sync off: %d", code)
	}
	t.Cleanup(func() {
		r := adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/durtoggle/o.txt", nil, ""))
		_ = r.Body.Close()
		r = adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/durtoggle", nil, ""))
		_ = r.Body.Close()
	})

	set(true)
	if !read() {
		t.Fatal("re-enable did not persist")
	}
}
