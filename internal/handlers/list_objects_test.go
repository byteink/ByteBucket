package handlers

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// seedListBucket places ObjectsRoot under a temp dir and writes each provided
// key as a literal file with its full directory tree, mirroring the on-disk
// layout used by UploadObjectHandler in production. Returns the temp root.
func seedListBucket(t *testing.T, bucket string, keys []string) string {
	t.Helper()
	dir := t.TempDir()
	origStore, origHandler := storage.ObjectsRoot, objectsRoot
	storage.ObjectsRoot = dir
	objectsRoot = dir
	t.Cleanup(func() {
		storage.ObjectsRoot = origStore
		objectsRoot = origHandler
	})
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	for _, k := range keys {
		p := filepath.Join(dir, bucket, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", k, err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", k, err)
		}
	}
	return dir
}

// listJSON invokes ListObjectsHandler against the admin JSON surface and
// returns the parsed body. The /api prefix flips wantsJSON.
func listJSON(t *testing.T, bucket, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	url := "/api/s3/" + bucket
	if query != "" {
		url += "?" + query
	}
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}}
	ListObjectsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	return out
}

func extractKeys(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["contents"].([]any)
	if !ok || raw == nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("content entry not a map: %T", r)
		}
		out = append(out, m["key"].(string))
	}
	return out
}

func extractPrefixes(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["commonPrefixes"].([]any)
	if !ok || raw == nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		m := r.(map[string]any)
		out = append(out, m["prefix"].(string))
	}
	return out
}

func TestListObjects_RecursesIntoSubdirectories(t *testing.T) {
	seedListBucket(t, "b1", []string{"folder/file.txt", "root.txt", "deep/nest/inner.bin"})
	body := listJSON(t, "b1", "")
	got := extractKeys(t, body)
	sort.Strings(got)
	want := []string{"deep/nest/inner.bin", "folder/file.txt", "root.txt"}
	if !equal(got, want) {
		t.Fatalf("keys=%v want=%v", got, want)
	}
}

func TestListObjects_DelimiterRollsUpToCommonPrefixes(t *testing.T) {
	seedListBucket(t, "b1", []string{"folder/a.txt", "folder/b.txt", "root.txt"})
	body := listJSON(t, "b1", "delimiter=%2F")
	keys := extractKeys(t, body)
	prefixes := extractPrefixes(t, body)
	if !equal(keys, []string{"root.txt"}) {
		t.Fatalf("keys=%v want [root.txt]", keys)
	}
	if !equal(prefixes, []string{"folder/"}) {
		t.Fatalf("prefixes=%v want [folder/]", prefixes)
	}
}

func TestListObjects_PrefixAndDelimiter(t *testing.T) {
	seedListBucket(t, "b1", []string{"folder/a.txt", "folder/b.txt", "folder/sub/c.txt", "root.txt"})
	body := listJSON(t, "b1", "prefix=folder%2F&delimiter=%2F")
	keys := extractKeys(t, body)
	prefixes := extractPrefixes(t, body)
	sort.Strings(keys)
	sort.Strings(prefixes)
	if !equal(keys, []string{"folder/a.txt", "folder/b.txt"}) {
		t.Fatalf("keys=%v", keys)
	}
	if !equal(prefixes, []string{"folder/sub/"}) {
		t.Fatalf("prefixes=%v", prefixes)
	}
}

func TestListObjects_ExcludesSidecarsAtEveryDepth(t *testing.T) {
	dir := seedListBucket(t, "b1", []string{"folder/file.txt"})
	// Drop sidecars at root and nested. These should never appear in the
	// listing regardless of their depth, since they are server-internal.
	for _, name := range []string{".cors.json", ".acl.json", "folder/.cors.json", "folder/.acl.json", "folder/file.txt.meta"} {
		p := filepath.Join(dir, "b1", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Sidecars are JSON in production; write a minimal valid document
		// so any code path that *did* try to parse them would not fail
		// for the wrong reason. The point of this test is exclusion.
		if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	body := listJSON(t, "b1", "")
	keys := extractKeys(t, body)
	for _, k := range keys {
		if strings.HasSuffix(k, ".meta") || strings.HasSuffix(k, ".cors.json") || strings.HasSuffix(k, ".acl.json") {
			t.Fatalf("sidecar leaked: %s", k)
		}
	}
	if !equal(keys, []string{"folder/file.txt"}) {
		t.Fatalf("keys=%v want [folder/file.txt]", keys)
	}
}

func TestListObjects_MaxKeysAndContinuationToken(t *testing.T) {
	// Five nested keys; page two at a time, follow the token across pages.
	seedListBucket(t, "b1", []string{
		"a/1.txt", "a/2.txt", "b/3.txt", "b/4.txt", "c/5.txt",
	})

	body := listJSON(t, "b1", "max-keys=2")
	keys := extractKeys(t, body)
	if !equal(keys, []string{"a/1.txt", "a/2.txt"}) {
		t.Fatalf("page1 keys=%v", keys)
	}
	if got, _ := body["isTruncated"].(bool); !got {
		t.Fatalf("page1 should be truncated")
	}
	token, _ := body["nextContinuationToken"].(string)
	if token == "" {
		t.Fatalf("expected nextContinuationToken on truncated page")
	}

	body = listJSON(t, "b1", "max-keys=2&continuation-token="+token)
	keys = extractKeys(t, body)
	if !equal(keys, []string{"b/3.txt", "b/4.txt"}) {
		t.Fatalf("page2 keys=%v", keys)
	}
	token2, _ := body["nextContinuationToken"].(string)

	body = listJSON(t, "b1", "max-keys=2&continuation-token="+token2)
	keys = extractKeys(t, body)
	if !equal(keys, []string{"c/5.txt"}) {
		t.Fatalf("page3 keys=%v", keys)
	}
	if got, _ := body["isTruncated"].(bool); got {
		t.Fatalf("page3 should not be truncated")
	}
}

func TestListObjects_XMLShapeSurfacesNestedKeys(t *testing.T) {
	seedListBucket(t, "b1", []string{"folder/file.txt"})

	// Hit the SigV4 surface (no /api prefix) so the response is XML.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/b1", nil)
	c.Params = gin.Params{{Key: "bucket", Value: "b1"}}
	ListObjectsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var got struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("xml: %v body=%s", err, w.Body.String())
	}
	if len(got.Contents) != 1 || got.Contents[0].Key != "folder/file.txt" {
		t.Fatalf("xml contents=%+v", got.Contents)
	}
}

func TestListObjects_MissingBucketReturns404(t *testing.T) {
	seedListBucket(t, "exists", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/s3/ghost", nil)
	c.Params = gin.Params{{Key: "bucket", Value: "ghost"}}
	ListObjectsHandler(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
