package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// newDeleteObjectsEngine wires only the POST /:bucket?delete route the way the
// production dispatcher does, with no auth: the handler carries the security
// contract (per-key validation) that is under test here.
func newDeleteObjectsEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/:bucket", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["delete"]; ok {
			DeleteObjectsHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

func writeObj(t *testing.T, dir, bucket, key, body string) {
	t.Helper()
	p := filepath.Join(dir, bucket, key)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func postDeleteXML(t *testing.T, r *gin.Engine, bucket, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/"+bucket+"?delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDeleteObjects_RemovesAndReportsDeleted(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "a.txt", "a")
	writeObj(t, dir, "b1", "nested/b.txt", "b")

	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1",
		`<Delete><Object><Key>a.txt</Key></Object><Object><Key>nested/b.txt</Key></Object></Delete>`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	for _, k := range []string{"a.txt", "nested/b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "b1", k)); !os.IsNotExist(err) {
			t.Fatalf("%s should be deleted, stat err=%v", k, err)
		}
		if !strings.Contains(w.Body.String(), "<Key>"+k+"</Key>") {
			t.Fatalf("response missing Deleted for %s: %s", k, w.Body.String())
		}
	}
}

func TestDeleteObjects_QuietOmitsDeleted(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "a.txt", "a")

	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1",
		`<Delete><Quiet>true</Quiet><Object><Key>a.txt</Key></Object></Delete>`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "<Deleted>") {
		t.Fatalf("quiet mode must omit Deleted entries: %s", w.Body.String())
	}
}

func TestDeleteObjects_MissingKeyIsSuccess(t *testing.T) {
	seedACLBucket(t, "b1")
	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1",
		`<Delete><Object><Key>ghost.txt</Key></Object></Delete>`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<Key>ghost.txt</Key>") || strings.Contains(w.Body.String(), "<Error>") {
		t.Fatalf("missing key must report as Deleted, not Error: %s", w.Body.String())
	}
}

// Security: a traversal key must be rejected per-key and must NOT remove any
// file outside the bucket. A real sibling object must survive.
func TestDeleteObjects_RejectsTraversalKey(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "keep.txt", "keep")
	outside := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1",
		`<Delete><Object><Key>../secret.txt</Key></Object><Object><Key>keep.txt</Key></Object></Delete>`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("traversal deleted an out-of-bucket file: %v", err)
	}
	if !strings.Contains(w.Body.String(), "<Error>") {
		t.Fatalf("traversal key must report an Error entry: %s", w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "b1", "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("valid sibling key must still be deleted independently, stat err=%v", err)
	}
}

// Security: a key naming a bucket sidecar must be rejected so a batch delete
// cannot strip a bucket's ACL/CORS config.
func TestDeleteObjects_RejectsSidecarKey(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	sidecar := filepath.Join(dir, "b1", ".acl.json")
	if err := os.WriteFile(sidecar, []byte(`{"canned":"private"}`), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1",
		`<Delete><Object><Key>.acl.json</Key></Object></Delete>`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar must not be deletable via batch key: %v", err)
	}
	if !strings.Contains(w.Body.String(), "<Error>") {
		t.Fatalf("sidecar key must report an Error: %s", w.Body.String())
	}
}

func TestDeleteObjects_EmptyListRejected(t *testing.T) {
	seedACLBucket(t, "b1")
	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1", `<Delete></Delete>`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty delete must be 400, got %d", w.Code)
	}
}

func TestDeleteObjects_TooManyKeysRejected(t *testing.T) {
	seedACLBucket(t, "b1")
	var b strings.Builder
	b.WriteString("<Delete>")
	for i := 0; i < maxDeleteKeys+1; i++ {
		b.WriteString("<Object><Key>k")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString("</Key></Object>")
	}
	b.WriteString("</Delete>")
	w := postDeleteXML(t, newDeleteObjectsEngine(), "b1", b.String())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("over-limit delete must be 400, got %d", w.Code)
	}
}

func TestDeleteObjects_JSONSurface(t *testing.T) {
	dir := seedACLBucket(t, "b1")
	writeObj(t, dir, "b1", "a.txt", "a")

	req := httptest.NewRequest(http.MethodPost, "/b1?delete", bytes.NewReader([]byte(`{"objects":["a.txt"]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	newDeleteObjectsEngine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Deleted) != 1 || resp.Deleted[0] != "a.txt" {
		t.Fatalf("unexpected deleted: %+v", resp.Deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "b1", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("object not removed: %v", err)
	}
}
