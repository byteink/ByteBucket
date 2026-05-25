package handlers

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// newTaggingTestEngine wires the object tagging handlers behind the same
// query-subresource dispatch the production router uses. No auth middleware is
// attached: the handlers carry the contract under test.
func newTaggingTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket/*objectKey", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["tagging"]; ok {
			PutObjectTaggingHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.GET("/:bucket/*objectKey", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["tagging"]; ok {
			GetObjectTaggingHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	r.DELETE("/:bucket/*objectKey", func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["tagging"]; ok {
			DeleteObjectTaggingHandler(c)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

// seedTaggingObject points both roots at a temp dir, creates the bucket, and
// writes one object. Returns the temp root so callers can inspect sidecars.
func seedTaggingObject(t *testing.T, bucket, key string) string {
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
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, bucket, key), []byte("body"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	return dir
}

func putTagging(t *testing.T, r *gin.Engine, url, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader([]byte(body)))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const validTaggingXML = `<Tagging><TagSet>` +
	`<Tag><Key>env</Key><Value>prod</Value></Tag>` +
	`<Tag><Key>team</Key><Value>core</Value></Tag>` +
	`</TagSet></Tagging>`

func TestPutGetDeleteObjectTagging_XMLRoundTrip(t *testing.T) {
	seedTaggingObject(t, "b1", "k")
	r := newTaggingTestEngine()

	if w := putTagging(t, r, "/b1/k?tagging", "application/xml", validTaggingXML); w.Code != http.StatusOK {
		t.Fatalf("put: %d body=%s", w.Code, w.Body.String())
	}

	// GET on the SigV4 (XML) surface returns the <Tagging> document.
	getReq := httptest.NewRequest(http.MethodGet, "/b1/k?tagging", nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get: %d", getW.Code)
	}
	var doc s3Tagging
	if err := xml.Unmarshal(getW.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v body=%s", err, getW.Body.String())
	}
	if len(doc.TagSet.Tags) != 2 || doc.TagSet.Tags[0].Key != "env" || doc.TagSet.Tags[0].Value != "prod" {
		t.Fatalf("unexpected tag set: %+v", doc.TagSet.Tags)
	}

	// DELETE clears and returns 204; a subsequent GET is an empty set.
	delReq := httptest.NewRequest(http.MethodDelete, "/b1/k?tagging", nil)
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", delW.Code)
	}
	getW2 := httptest.NewRecorder()
	r.ServeHTTP(getW2, httptest.NewRequest(http.MethodGet, "/b1/k?tagging", nil))
	var doc2 s3Tagging
	if err := xml.Unmarshal(getW2.Body.Bytes(), &doc2); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if len(doc2.TagSet.Tags) != 0 {
		t.Fatalf("expected empty set after delete, got %+v", doc2.TagSet.Tags)
	}
}

func TestPutObjectTagging_JSONSurface(t *testing.T) {
	seedTaggingObject(t, "b1", "k")
	r := newTaggingTestEngine()

	if w := putTagging(t, r, "/b1/k?tagging", "application/json",
		`{"tagSet":[{"key":"a","value":"1"}]}`); w.Code != http.StatusOK {
		t.Fatalf("put json: %d body=%s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/b1/k?tagging", nil)
	getReq.Header.Set("Accept", "application/json")
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	var body adminTagging
	if err := json.Unmarshal(getW.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.TagSet) != 1 || body.TagSet[0].Key != "a" || body.TagSet[0].Value != "1" {
		t.Fatalf("json round-trip: %+v", body.TagSet)
	}
}

func TestPutObjectTagging_Rejections(t *testing.T) {
	manyTags := "<Tagging><TagSet>"
	for i := 0; i < storage.MaxObjectTags+1; i++ {
		manyTags += "<Tag><Key>k" + string(rune('a'+i)) + "</Key><Value>v</Value></Tag>"
	}
	manyTags += "</TagSet></Tagging>"

	longKey := "<Tagging><TagSet><Tag><Key>" + strings.Repeat("k", storage.MaxTagKeyLen+1) +
		"</Key><Value>v</Value></Tag></TagSet></Tagging>"
	longVal := "<Tagging><TagSet><Tag><Key>k</Key><Value>" + strings.Repeat("v", storage.MaxTagValueLen+1) +
		"</Value></Tag></TagSet></Tagging>"
	dupKey := "<Tagging><TagSet>" +
		"<Tag><Key>k</Key><Value>1</Value></Tag>" +
		"<Tag><Key>k</Key><Value>2</Value></Tag></TagSet></Tagging>"

	cases := map[string]struct {
		body     string
		wantCode string
	}{
		"too many tags":  {manyTags, "InvalidTag"},
		"key too long":   {longKey, "InvalidTag"},
		"value too long": {longVal, "InvalidTag"},
		"duplicate key":  {dupKey, "InvalidTag"},
		"malformed xml":  {"<Tagging><TagSet><Tag><Key>k", "MalformedXML"},
		"not xml":        {"this is not xml at all", "MalformedXML"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			seedTaggingObject(t, "b1", "k")
			r := newTaggingTestEngine()
			w := putTagging(t, r, "/b1/k?tagging", "application/xml", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantCode) {
				t.Fatalf("expected %s, got %s", tc.wantCode, w.Body.String())
			}
		})
	}
}

// TestPutObjectTagging_OversizedBodyRejected proves the bounded read rejects a
// body past the cap before parsing, so a hostile client cannot make the server
// buffer an unbounded document.
func TestPutObjectTagging_OversizedBodyRejected(t *testing.T) {
	seedTaggingObject(t, "b1", "k")
	r := newTaggingTestEngine()
	huge := "<Tagging><TagSet>" + strings.Repeat("<Tag><Key>k</Key><Value>v</Value></Tag>", 5000) + "</TagSet></Tagging>"
	w := putTagging(t, r, "/b1/k?tagging", "application/xml", huge)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d", w.Code)
	}
}

func TestPutObjectTagging_NoSuchKey(t *testing.T) {
	seedTaggingObject(t, "b1", "k")
	r := newTaggingTestEngine()
	w := putTagging(t, r, "/b1/missing?tagging", "application/xml", validTaggingXML)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "NoSuchKey") {
		t.Fatalf("expected NoSuchKey, got %s", w.Body.String())
	}
}

// TestObjectTagging_DoesNotChangeETag proves a tag set/clear cycle leaves the
// object's .meta sidecar (which holds the ETag and Content-Type) untouched.
func TestObjectTagging_DoesNotChangeETag(t *testing.T) {
	dir := seedTaggingObject(t, "b1", "k")
	metaPath := filepath.Join(dir, "b1", "k.meta")
	if err := os.WriteFile(metaPath, []byte(`{"etag":"deadbeef","content-type":"text/plain"}`), 0644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	metaBefore, _ := os.ReadFile(metaPath)
	objBefore, _ := os.ReadFile(filepath.Join(dir, "b1", "k"))

	r := newTaggingTestEngine()
	if w := putTagging(t, r, "/b1/k?tagging", "application/xml", validTaggingXML); w.Code != http.StatusOK {
		t.Fatalf("put: %d", w.Code)
	}
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, httptest.NewRequest(http.MethodDelete, "/b1/k?tagging", nil))

	metaAfter, _ := os.ReadFile(metaPath)
	objAfter, _ := os.ReadFile(filepath.Join(dir, "b1", "k"))
	if string(metaBefore) != string(metaAfter) {
		t.Fatalf("tagging mutated .meta (ETag/Content-Type)")
	}
	if string(objBefore) != string(objAfter) {
		t.Fatalf("tagging mutated object bytes")
	}
}

// TestObjectTagging_SidecarHiddenFromListing proves the .tags.json sidecar is
// filtered out of ListObjects at the object's depth.
func TestObjectTagging_SidecarHiddenFromListing(t *testing.T) {
	seedTaggingObject(t, "b1", "k")
	r := newTaggingTestEngine()
	if w := putTagging(t, r, "/b1/k?tagging", "application/xml", validTaggingXML); w.Code != http.StatusOK {
		t.Fatalf("put: %d", w.Code)
	}
	body := listJSON(t, "b1", "")
	for _, key := range extractKeys(t, body) {
		if strings.HasSuffix(key, ".tags.json") {
			t.Fatalf(".tags.json sidecar leaked into listing: %v", extractKeys(t, body))
		}
	}
	keys := extractKeys(t, body)
	if len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("expected only object key 'k', got %v", keys)
	}
}
