package handlers

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// listXMLRaw hits the SigV4 (XML) surface and returns the decoded body into
// the caller-supplied struct, asserting 200.
func listXMLRaw(t *testing.T, bucket, query string, out any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	url := "/" + bucket
	if query != "" {
		url += "?" + query
	}
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Params = gin.Params{{Key: "bucket", Value: bucket}}
	ListObjectsHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := xml.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("xml: %v body=%s", err, w.Body.String())
	}
}

// V2 emits KeyCount + (Next)ContinuationToken + StartAfter and never Marker.
func TestListObjects_V2StrictShape(t *testing.T) {
	seedListBucket(t, "b1", []string{"a/1.txt", "a/2.txt", "b/3.txt"})

	var got struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		KeyCount              int      `xml:"KeyCount"`
		MaxKeys               int      `xml:"MaxKeys"`
		IsTruncated           bool     `xml:"IsTruncated"`
		Marker                string   `xml:"Marker"`
		NextContinuationToken string   `xml:"NextContinuationToken"`
		Contents              []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	// max-keys=2 over three keys forces truncation on page one.
	listXMLRaw(t, "b1", "list-type=2&max-keys=2", &got)

	if got.KeyCount != 2 {
		t.Fatalf("KeyCount=%d want 2", got.KeyCount)
	}
	if !got.IsTruncated {
		t.Fatalf("expected IsTruncated on page one")
	}
	if got.NextContinuationToken == "" {
		t.Fatalf("expected NextContinuationToken on truncated v2 page")
	}
	if got.Marker != "" {
		t.Fatalf("v2 must not emit Marker, got %q", got.Marker)
	}

	// Follow the token; the second page must resume after the first two keys.
	var page2 struct {
		XMLName           xml.Name `xml:"ListBucketResult"`
		IsTruncated       bool     `xml:"IsTruncated"`
		ContinuationToken string   `xml:"ContinuationToken"`
		Contents          []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	listXMLRaw(t, "b1", "list-type=2&max-keys=2&continuation-token="+got.NextContinuationToken, &page2)
	if page2.ContinuationToken != got.NextContinuationToken {
		t.Fatalf("ContinuationToken not echoed: got %q", page2.ContinuationToken)
	}
	if len(page2.Contents) != 1 || page2.Contents[0].Key != "b/3.txt" {
		t.Fatalf("page2 contents=%+v want [b/3.txt]", page2.Contents)
	}
	if page2.IsTruncated {
		t.Fatalf("page2 should not be truncated")
	}
}

// V2 start-after is a literal cursor, echoed verbatim, dropping keys <= it.
func TestListObjects_V2StartAfter(t *testing.T) {
	seedListBucket(t, "b1", []string{"a.txt", "b.txt", "c.txt"})
	var got struct {
		XMLName    xml.Name `xml:"ListBucketResult"`
		StartAfter string   `xml:"StartAfter"`
		Contents   []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	listXMLRaw(t, "b1", "list-type=2&start-after=a.txt", &got)
	if got.StartAfter != "a.txt" {
		t.Fatalf("StartAfter=%q want a.txt", got.StartAfter)
	}
	keys := []string{}
	for _, c := range got.Contents {
		keys = append(keys, c.Key)
	}
	if !equal(keys, []string{"b.txt", "c.txt"}) {
		t.Fatalf("keys=%v want [b.txt c.txt]", keys)
	}
}

// V1 pages by Marker, emits NextMarker only when a delimiter is set, and never
// emits the v2-only KeyCount/ContinuationToken fields.
func TestListObjects_V1MarkerAndNextMarker(t *testing.T) {
	seedListBucket(t, "b1", []string{"d1/1.txt", "d2/2.txt", "d3/3.txt"})

	// With a delimiter, three folders roll up to three common prefixes; page
	// two at a time so page one truncates and must carry NextMarker.
	var got struct {
		XMLName        xml.Name `xml:"ListBucketResult"`
		Marker         string   `xml:"Marker"`
		NextMarker     string   `xml:"NextMarker"`
		IsTruncated    bool     `xml:"IsTruncated"`
		KeyCount       *int     `xml:"KeyCount"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}
	listXMLRaw(t, "b1", "max-keys=2&delimiter=%2F", &got)
	if !got.IsTruncated {
		t.Fatalf("expected truncation")
	}
	if got.NextMarker != "d2/" {
		t.Fatalf("NextMarker=%q want d2/", got.NextMarker)
	}
	if got.KeyCount != nil {
		t.Fatalf("v1 must not emit KeyCount, got %v", *got.KeyCount)
	}

	// Resume from the marker; the final folder must appear.
	var page2 struct {
		XMLName        xml.Name `xml:"ListBucketResult"`
		Marker         string   `xml:"Marker"`
		IsTruncated    bool     `xml:"IsTruncated"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}
	listXMLRaw(t, "b1", "max-keys=2&delimiter=%2F&marker="+got.NextMarker, &page2)
	if page2.Marker != "d2/" {
		t.Fatalf("Marker not echoed: %q", page2.Marker)
	}
	if len(page2.CommonPrefixes) != 1 || page2.CommonPrefixes[0].Prefix != "d3/" {
		t.Fatalf("page2 prefixes=%+v want [d3/]", page2.CommonPrefixes)
	}
	if page2.IsTruncated {
		t.Fatalf("page2 should not be truncated")
	}
}

// listAllPages walks the JSON surface page by page exactly as the UI does:
// follow nextContinuationToken until the listing reports it is no longer
// truncated, accumulating keys and prefixes. Returns the union and asserts no
// page is ever truncated without issuing a token (which would strand items).
func listAllPages(t *testing.T, bucket, baseQuery string, maxKeys int) (keys, prefixes []string) {
	t.Helper()
	token := ""
	seenK := map[string]bool{}
	seenP := map[string]bool{}
	for page := 0; page < 10000; page++ {
		q := baseQuery + "&max-keys=" + strconv.Itoa(maxKeys)
		if token != "" {
			q += "&continuation-token=" + token
		}
		body := listJSON(t, bucket, strings.TrimPrefix(q, "&"))
		for _, k := range extractKeys(t, body) {
			if seenK[k] {
				t.Fatalf("duplicate object across pages: %q", k)
			}
			seenK[k] = true
			keys = append(keys, k)
		}
		for _, p := range extractPrefixes(t, body) {
			if seenP[p] {
				t.Fatalf("duplicate prefix across pages: %q", p)
			}
			seenP[p] = true
			prefixes = append(prefixes, p)
		}
		trunc, _ := body["isTruncated"].(bool)
		token, _ = body["nextContinuationToken"].(string)
		if !trunc {
			if token != "" {
				t.Fatalf("not truncated but token present: %q", token)
			}
			return keys, prefixes
		}
		if token == "" {
			t.Fatalf("page %d truncated but no continuation token (items stranded)", page)
		}
	}
	t.Fatal("pagination did not terminate")
	return nil, nil
}

// Paginating a large, mixed (files + folders) listing across many small pages
// must enumerate every top-level entry exactly once, for any page size.
func TestListObjects_PaginationLosesNothing(t *testing.T) {
	var seed []string
	wantFiles := map[string]bool{}
	wantFolders := map[string]bool{}
	// 50 top-level files and 40 folders of 30 objects each: 1250 items total,
	// well past the 1000 ceiling, with files and folder-prefixes interleaving
	// lexically so page boundaries fall inside both arms.
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("file-%03d.txt", i)
		seed = append(seed, k)
		wantFiles[k] = true
	}
	for f := 0; f < 40; f++ {
		folder := fmt.Sprintf("dir-%03d/", f)
		wantFolders[folder] = true
		for o := 0; o < 30; o++ {
			seed = append(seed, fmt.Sprintf("%sobj-%03d.bin", folder, o))
		}
	}
	seedListBucket(t, "big", seed)

	for _, mk := range []int{1, 2, 7, 100, 1000} {
		keys, prefixes := listAllPages(t, "big", "delimiter=%2F", mk)
		if len(keys) != len(wantFiles) {
			t.Fatalf("maxKeys=%d: got %d top-level files, want %d", mk, len(keys), len(wantFiles))
		}
		for _, k := range keys {
			if !wantFiles[k] {
				t.Fatalf("maxKeys=%d: unexpected file %q", mk, k)
			}
		}
		if len(prefixes) != len(wantFolders) {
			t.Fatalf("maxKeys=%d: got %d folders, want %d", mk, len(prefixes), len(wantFolders))
		}
		for _, p := range prefixes {
			if !wantFolders[p] {
				t.Fatalf("maxKeys=%d: unexpected folder %q", mk, p)
			}
		}
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
