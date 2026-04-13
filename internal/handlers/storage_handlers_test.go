package handlers

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withTempObjectsRoot redirects the package-level objectsRoot at a temp dir
// for the life of the test, so handler tests do not touch /data.
func withTempObjectsRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := objectsRoot
	objectsRoot = dir
	t.Cleanup(func() { objectsRoot = orig })
	return dir
}

// ListBucketsHandler must echo the authenticated caller as Owner. Hardcoding
// a placeholder is user-visible via any S3 SDK's ListBuckets response, and
// breaks downstream tooling that gates on the owner identity.
func TestListBucketsHandlerUsesAuthenticatedOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := withTempObjectsRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set("user", &storage.User{AccessKeyID: "AKIAEXAMPLE"})

	ListBucketsHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if strings.Contains(body, "dummy-owner") || strings.Contains(body, "dummy-owner-id") {
		t.Fatalf("response still contains placeholder owner: %s", body)
	}

	var parsed struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("xml parse: %v; body=%s", err, body)
	}
	if parsed.Owner.ID != "AKIAEXAMPLE" || parsed.Owner.DisplayName != "AKIAEXAMPLE" {
		t.Fatalf("owner = {%q,%q}; want AKIAEXAMPLE in both",
			parsed.Owner.ID, parsed.Owner.DisplayName)
	}
}
