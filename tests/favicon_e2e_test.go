package tests

import (
	"net/http"
	"strings"
	"testing"
)

// TestE2E_StorageFavicon proves the storage surface serves a favicon: a browser
// opening a public-object link on the storage origin probes /favicon.ico and must
// get the icon, not the bucket dispatcher's 400 on the invalid bucket name. The
// containerised binary embeds the built UI bundle, so the icon is present.
func TestE2E_StorageFavicon(t *testing.T) {
	resp, err := http.Get(storageURL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET /favicon.ico: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("favicon status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "icon") {
		t.Fatalf("favicon content-type = %q, want an icon type", ct)
	}
}
