package tests

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Cross-surface parity E2E tests.
//
// The refactor mounts the same S3 storage handlers on two routers: SigV4
// at "/" on port 9000 (XML wire format) and admin-header-auth at "/s3/*"
// on port 9001 (JSON wire format). These tests assert that for every op,
// what one surface writes the other can read, that content-addressable
// properties (ETag, body bytes) are byte-for-byte identical, and that
// per-bucket CORS rules set on one surface are honoured on the other —
// including preflight enforcement.
//
// All subtests share the single testcontainers stack brought up in
// TestMain; unique bucket names (time.Now().UnixNano()) isolate them.

// Shared constants so the linter's "repeated literal" warning stays quiet
// and the intent is explicit: every Content-Type, every header name and
// every error-message format string used by more than one subtest lives
// here, not inline in each scenario.
const (
	hdrContentType     = "Content-Type"
	hdrACReqMethod     = "Access-Control-Request-Method"
	hdrACAllowOrigin   = "Access-Control-Allow-Origin"
	ctJSON             = "application/json"
	ctXML              = "application/xml"
	originTrusted      = "http://trusted.example"
	fmtAdminPutCORSErr = "admin PUT cors = %d"
)

// --- admin-surface helpers. Thin wrappers over net/http that attach the
// admin credentials and target /s3/* on the admin router. SigV4 helpers
// are reused from auth_security_test.go (buildHeaderSigned, sigV4Request).

// adminDo sends a pre-built request with admin headers attached.
func adminDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin do %s %s: %v", req.Method, req.URL.String(), err)
	}
	return resp
}

// adminRequest builds an authenticated request against the admin /s3
// surface. Body is optional; contentType is ignored when body is nil.
func adminRequest(t *testing.T, method, path string, body []byte, contentType string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, adminURL+path, r)
	if err != nil {
		t.Fatalf("admin new request: %v", err)
	}
	if body != nil && contentType != "" {
		req.Header.Set(hdrContentType, contentType)
	}
	return req
}

// sigV4Do builds a SigV4-signed request against the storage router and
// executes it. Kept local so the test reads linearly; buildHeaderSigned
// itself is reused from auth_security_test.go.
func sigV4Do(t *testing.T, method, path string, body []byte, extra map[string]string) *http.Response {
	t.Helper()
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: method, path: path, body: body,
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
		extraHeaders: extra,
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sigv4 do %s %s: %v", method, path, err)
	}
	return resp
}

// readAllClose returns body bytes and ensures Close errors are surfaced.
// The user's style rejects silent `_ = resp.Body.Close()` where a real
// error would otherwise be hidden, so we log closer errors via t.Errorf
// without aborting the test (close failures after successful read are
// diagnostic only).
func readAllClose(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

// assertSameBody fails the test if two byte slices differ, logging the
// lengths and first divergence index for quick triage.
func assertSameBody(t *testing.T, a, b []byte, label string) {
	t.Helper()
	if bytes.Equal(a, b) {
		return
	}
	idx := -1
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			idx = i
			break
		}
	}
	t.Fatalf("%s: bodies differ (len a=%d b=%d, first diff at %d)", label, len(a), len(b), idx)
}

// randomBytes returns n cryptographically random bytes. Deterministic
// content is avoided so an accidental hash collision in a test artefact
// would not mask a real round-trip bug.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return buf
}

// wireETag returns the S3 wire-format ETag ("<hex-md5>") for body bytes.
func wireETag(body []byte) string {
	sum := md5.Sum(body)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

// uniqueBucket makes a collision-safe bucket name scoped to a subtest.
func uniqueBucket(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// --- response parsing shapes local to these tests. We do NOT import the
// handler structs; tests pin the wire format, not the Go type layout.

type xmlListAllMyBuckets struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   struct {
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName"`
	} `xml:"Owner"`
	Buckets struct {
		Bucket []struct {
			Name string `xml:"Name"`
		} `xml:"Bucket"`
	} `xml:"Buckets"`
}

type jsonListBuckets struct {
	Buckets []struct {
		Name string `json:"name"`
	} `json:"buckets"`
}

type xmlListBucketResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key  string `xml:"Key"`
		ETag string `xml:"ETag"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
}

type jsonListObjects struct {
	Contents []struct {
		Key  string `json:"key"`
		ETag string `json:"etag"`
		Size int64  `json:"size"`
	} `json:"contents"`
}

type jsonCORSConfig struct {
	CORSRules []struct {
		ID             string   `json:"ID,omitempty"`
		AllowedMethods []string `json:"AllowedMethods"`
		AllowedOrigins []string `json:"AllowedOrigins"`
		AllowedHeaders []string `json:"AllowedHeaders,omitempty"`
		ExposeHeaders  []string `json:"ExposeHeaders,omitempty"`
		MaxAgeSeconds  int      `json:"MaxAgeSeconds,omitempty"`
	} `json:"CORSRules"`
}

type xmlCORSConfiguration struct {
	XMLName   xml.Name `xml:"CORSConfiguration"`
	CORSRules []struct {
		ID            string   `xml:"ID,omitempty"`
		AllowedMethod []string `xml:"AllowedMethod"`
		AllowedOrigin []string `xml:"AllowedOrigin"`
		AllowedHeader []string `xml:"AllowedHeader,omitempty"`
		ExposeHeader  []string `xml:"ExposeHeader,omitempty"`
		MaxAgeSeconds int      `xml:"MaxAgeSeconds,omitempty"`
	} `xml:"CORSRule"`
}

// --- small helpers that exercise a single cross-surface action so each
// subtest reads like a scenario, not plumbing.

func putBucketAdmin(t *testing.T, bucket string) {
	t.Helper()
	resp := adminDo(t, adminRequest(t, http.MethodPut, "/s3/"+bucket, nil, ""))
	_ = readAllClose(t, resp)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("admin PUT bucket %s = %d", bucket, resp.StatusCode)
	}
}

func putBucketSigV4(t *testing.T, bucket string) {
	t.Helper()
	resp := sigV4Do(t, http.MethodPut, "/"+bucket, nil, nil)
	_ = readAllClose(t, resp)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("sigv4 PUT bucket %s = %d", bucket, resp.StatusCode)
	}
}

func putObjectAdmin(t *testing.T, bucket, key string, body []byte) *http.Response {
	t.Helper()
	resp := adminDo(t, adminRequest(t, http.MethodPut, "/s3/"+bucket+"/"+key, body, "application/octet-stream"))
	if resp.StatusCode/100 != 2 {
		t.Fatalf("admin PUT obj = %d", resp.StatusCode)
	}
	return resp
}

func putObjectSigV4(t *testing.T, bucket, key string, body []byte) *http.Response {
	t.Helper()
	resp := sigV4Do(t, http.MethodPut, "/"+bucket+"/"+key, body, nil)
	if resp.StatusCode/100 != 2 {
		_ = readAllClose(t, resp)
		t.Fatalf("sigv4 PUT obj = %d", resp.StatusCode)
	}
	return resp
}

func TestCrossSurfaceParity(t *testing.T) {
	t.Run("BucketCreateOn9001_ListOn9000", func(t *testing.T) {
		bucket := uniqueBucket("parity-a")
		putBucketAdmin(t, bucket)

		resp := sigV4Do(t, http.MethodGet, "/", nil, nil)
		body := readAllClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list buckets sigv4 = %d: %s", resp.StatusCode, body)
		}
		var parsed xmlListAllMyBuckets
		if err := xml.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("xml: %v; body=%s", err, body)
		}
		found := false
		for _, b := range parsed.Buckets.Bucket {
			if b.Name == bucket {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bucket %q created via admin not visible on SigV4 list: %s", bucket, body)
		}
		if parsed.Owner.ID != adminCreds.AccessKeyID {
			t.Fatalf("Owner.ID = %q; want authenticated access key %q",
				parsed.Owner.ID, adminCreds.AccessKeyID)
		}
	})

	t.Run("BucketCreateOn9000_ListOn9001", func(t *testing.T) {
		bucket := uniqueBucket("parity-b")
		putBucketSigV4(t, bucket)

		resp := adminDo(t, adminRequest(t, http.MethodGet, "/s3/", nil, ""))
		body := readAllClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list buckets admin = %d: %s", resp.StatusCode, body)
		}
		var parsed jsonListBuckets
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("json: %v; body=%s", err, body)
		}
		found := false
		for _, b := range parsed.Buckets {
			if b.Name == bucket {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bucket %q created via SigV4 not visible on admin list: %s", bucket, body)
		}
	})

	t.Run("ObjectPutOn9001_GetOn9000", func(t *testing.T) {
		bucket := uniqueBucket("parity-c")
		putBucketAdmin(t, bucket)

		payload := randomBytes(t, 8*1024)
		want := wireETag(payload)

		putResp := putObjectAdmin(t, bucket, "blob.bin", payload)
		_ = readAllClose(t, putResp)
		if got := putResp.Header.Get("ETag"); got != want {
			t.Fatalf("admin PUT ETag = %q; want %q", got, want)
		}

		getResp := sigV4Do(t, http.MethodGet, "/"+bucket+"/blob.bin", nil, nil)
		got := readAllClose(t, getResp)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("sigv4 GET = %d: %s", getResp.StatusCode, got)
		}
		assertSameBody(t, payload, got, "admin-put/sigv4-get")
		if e := getResp.Header.Get("ETag"); e != want {
			t.Fatalf("sigv4 GET ETag = %q; want %q", e, want)
		}
	})

	t.Run("ObjectPutOn9000_GetOn9001", func(t *testing.T) {
		bucket := uniqueBucket("parity-d")
		putBucketSigV4(t, bucket)

		payload := randomBytes(t, 8*1024)
		want := wireETag(payload)

		putResp := putObjectSigV4(t, bucket, "blob.bin", payload)
		_ = readAllClose(t, putResp)
		if got := putResp.Header.Get("ETag"); got != want {
			t.Fatalf("sigv4 PUT ETag = %q; want %q", got, want)
		}

		getResp := adminDo(t, adminRequest(t, http.MethodGet, "/s3/"+bucket+"/blob.bin", nil, ""))
		got := readAllClose(t, getResp)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("admin GET = %d: %s", getResp.StatusCode, got)
		}
		assertSameBody(t, payload, got, "sigv4-put/admin-get")
		if e := getResp.Header.Get("ETag"); e != want {
			t.Fatalf("admin GET ETag = %q; want %q", e, want)
		}
	})

	t.Run("ObjectPutOn9001_HeadOn9000", func(t *testing.T) {
		bucket := uniqueBucket("parity-e")
		putBucketAdmin(t, bucket)

		payload := randomBytes(t, 2048)
		want := wireETag(payload)
		putResp := putObjectAdmin(t, bucket, "small.bin", payload)
		_ = readAllClose(t, putResp)

		headResp := sigV4Do(t, http.MethodHead, "/"+bucket+"/small.bin", nil, nil)
		_ = readAllClose(t, headResp)
		if headResp.StatusCode != http.StatusOK {
			t.Fatalf("sigv4 HEAD = %d", headResp.StatusCode)
		}
		if e := headResp.Header.Get("ETag"); e != want {
			t.Fatalf("sigv4 HEAD ETag = %q; want %q", e, want)
		}
		if e := putResp.Header.Get("ETag"); e != want {
			t.Fatalf("admin PUT ETag = %q; want %q", e, want)
		}
		// Content-Length on HEAD must match the uploaded byte count so S3
		// clients can skip a ranged GET when they only need the size.
		wantLen := strconv.Itoa(len(payload))
		if got := headResp.Header.Get("Content-Length"); got != wantLen {
			t.Fatalf("sigv4 HEAD Content-Length = %q; want %q", got, wantLen)
		}
	})

	t.Run("ObjectListParity", func(t *testing.T) {
		bucket := uniqueBucket("parity-f")
		putBucketAdmin(t, bucket)

		type obj struct {
			key  string
			body []byte
		}
		entries := []obj{
			{"a.bin", randomBytes(t, 1024)},
			{"b.bin", randomBytes(t, 2048)},
			{"c.bin", randomBytes(t, 4096)},
		}
		for _, e := range entries {
			putResp := putObjectAdmin(t, bucket, e.key, e.body)
			_ = readAllClose(t, putResp)
		}

		// SigV4 XML listing.
		xmlResp := sigV4Do(t, http.MethodGet, "/"+bucket, nil, nil)
		xmlBody := readAllClose(t, xmlResp)
		if xmlResp.StatusCode != http.StatusOK {
			t.Fatalf("sigv4 list = %d: %s", xmlResp.StatusCode, xmlBody)
		}
		var xparsed xmlListBucketResult
		if err := xml.Unmarshal(xmlBody, &xparsed); err != nil {
			t.Fatalf("xml list: %v; %s", err, xmlBody)
		}
		xKeys := map[string]string{}
		for _, c := range xparsed.Contents {
			xKeys[c.Key] = c.ETag
		}

		// Admin JSON listing.
		jResp := adminDo(t, adminRequest(t, http.MethodGet, "/s3/"+bucket, nil, ""))
		jBody := readAllClose(t, jResp)
		if jResp.StatusCode != http.StatusOK {
			t.Fatalf("admin list = %d: %s", jResp.StatusCode, jBody)
		}
		var jparsed jsonListObjects
		if err := json.Unmarshal(jBody, &jparsed); err != nil {
			t.Fatalf("json list: %v; %s", err, jBody)
		}
		jKeys := map[string]string{}
		for _, c := range jparsed.Contents {
			jKeys[c.Key] = c.ETag
		}

		// Both surfaces must report the exact same {key: ETag} set. A
		// difference here would mean the two routers are resolving ETags
		// through divergent code paths — a real parity bug.
		for _, e := range entries {
			want := wireETag(e.body)
			if got := xKeys[e.key]; got != want {
				t.Fatalf("xml list ETag %s = %q; want %q", e.key, got, want)
			}
			if got := jKeys[e.key]; got != want {
				t.Fatalf("json list ETag %s = %q; want %q", e.key, got, want)
			}
		}
		if len(xKeys) != len(jKeys) {
			t.Fatalf("key-set size differs: xml=%v json=%v", xKeys, jKeys)
		}
	})

	t.Run("ObjectDeleteOn9001_AbsentOn9000", func(t *testing.T) {
		bucket := uniqueBucket("parity-g")
		putBucketAdmin(t, bucket)

		putResp := putObjectAdmin(t, bucket, "gone.txt", []byte("bye"))
		_ = readAllClose(t, putResp)

		delResp := adminDo(t, adminRequest(t, http.MethodDelete, "/s3/"+bucket+"/gone.txt", nil, ""))
		_ = readAllClose(t, delResp)
		if delResp.StatusCode != http.StatusNoContent {
			t.Fatalf("admin DELETE = %d", delResp.StatusCode)
		}

		getResp := sigV4Do(t, http.MethodGet, "/"+bucket+"/gone.txt", nil, nil)
		body := readAllClose(t, getResp)
		if getResp.StatusCode != http.StatusNotFound {
			t.Fatalf("sigv4 GET after delete = %d; want 404: %s", getResp.StatusCode, body)
		}
		if !strings.Contains(string(body), "NoSuchKey") {
			t.Fatalf("expected NoSuchKey in body, got %s", body)
		}
	})

	t.Run("RequestIDPresentOnBothSurfaces", func(t *testing.T) {
		// SigV4 surface.
		sResp := sigV4Do(t, http.MethodGet, "/", nil, nil)
		_ = readAllClose(t, sResp)
		sID := sResp.Header.Get("x-amz-request-id")
		if sID == "" {
			t.Fatalf("sigv4 missing x-amz-request-id")
		}

		// Admin surface.
		aResp := adminDo(t, adminRequest(t, http.MethodGet, "/s3/", nil, ""))
		_ = readAllClose(t, aResp)
		aID := aResp.Header.Get("x-amz-request-id")
		if aID == "" {
			t.Fatalf("admin missing x-amz-request-id")
		}

		if sID == aID {
			t.Fatalf("expected distinct request IDs across surfaces, got duplicate %q", sID)
		}
	})

	t.Run("PerBucketCORS_PutXMLOn9000_ReadJSONOn9001", func(t *testing.T) {
		bucket := uniqueBucket("parity-h")
		putBucketSigV4(t, bucket)

		const xmlBody = `<CORSConfiguration>
  <CORSRule>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedMethod>PUT</AllowedMethod>
    <AllowedOrigin>https://trusted.example</AllowedOrigin>
    <AllowedHeader>*</AllowedHeader>
    <ExposeHeader>ETag</ExposeHeader>
    <MaxAgeSeconds>600</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>`

		// PUT /:bucket?cors — the signer includes the query component in
		// the canonical request, so the ?cors subresource is part of what
		// we sign. Any attempt to attach ?cors after signing would fail
		// SigV4 with SignatureDoesNotMatch.
		putReq := buildHeaderSigned(t, storageURL, sigV4Request{
			method: http.MethodPut, path: "/" + bucket,
			body:         []byte(xmlBody),
			query:        parseQuery("cors="),
			accessKey:    adminCreds.AccessKeyID,
			secret:       adminCreds.SecretAccessKey,
			extraHeaders: map[string]string{hdrContentType: ctXML},
		})
		resp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			t.Fatalf("put cors xml: %v", err)
		}
		b := readAllClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sigv4 PUT cors = %d: %s", resp.StatusCode, b)
		}

		// Read back via admin JSON.
		getResp := adminDo(t, adminRequest(t, http.MethodGet, "/s3/"+bucket+"?cors", nil, ""))
		body := readAllClose(t, getResp)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("admin GET cors = %d: %s", getResp.StatusCode, body)
		}
		var parsed jsonCORSConfig
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("json: %v; %s", err, body)
		}
		if len(parsed.CORSRules) != 1 {
			t.Fatalf("rules = %d; want 1: %s", len(parsed.CORSRules), body)
		}
		rule := parsed.CORSRules[0]
		sort.Strings(rule.AllowedMethods)
		if !reflectEq(rule.AllowedMethods, []string{"GET", "PUT"}) {
			t.Fatalf("methods = %v; want [GET PUT]", rule.AllowedMethods)
		}
		if !reflectEq(rule.AllowedOrigins, []string{"https://trusted.example"}) {
			t.Fatalf("origins = %v", rule.AllowedOrigins)
		}
		if rule.MaxAgeSeconds != 600 {
			t.Fatalf("maxAge = %d", rule.MaxAgeSeconds)
		}
	})

	t.Run("PerBucketCORS_PutJSONOn9001_ReadXMLOn9000", func(t *testing.T) {
		bucket := uniqueBucket("parity-i")
		putBucketAdmin(t, bucket)

		body := `{"CORSRules":[{"AllowedMethods":["GET"],"AllowedOrigins":["https://ui.example"],"AllowedHeaders":["*"],"MaxAgeSeconds":120}]}`
		putResp := adminDo(t, adminRequest(t, http.MethodPut, "/s3/"+bucket+"?cors",
			[]byte(body), ctJSON))
		_ = readAllClose(t, putResp)
		if putResp.StatusCode != http.StatusOK {
			t.Fatalf(fmtAdminPutCORSErr, putResp.StatusCode)
		}

		// Read back via SigV4 XML.
		getReq := buildHeaderSigned(t, storageURL, sigV4Request{
			method: http.MethodGet, path: "/" + bucket,
			query:     parseQuery("cors="),
			accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
		})
		resp, err := http.DefaultClient.Do(getReq)
		if err != nil {
			t.Fatalf("sigv4 get cors: %v", err)
		}
		xb := readAllClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sigv4 GET cors = %d: %s", resp.StatusCode, xb)
		}
		var parsed xmlCORSConfiguration
		if err := xml.Unmarshal(xb, &parsed); err != nil {
			t.Fatalf("xml: %v; %s", err, xb)
		}
		if len(parsed.CORSRules) != 1 {
			t.Fatalf("rules = %d: %s", len(parsed.CORSRules), xb)
		}
		r := parsed.CORSRules[0]
		if !reflectEq(r.AllowedMethod, []string{"GET"}) {
			t.Fatalf("methods = %v", r.AllowedMethod)
		}
		if !reflectEq(r.AllowedOrigin, []string{"https://ui.example"}) {
			t.Fatalf("origins = %v", r.AllowedOrigin)
		}
		if r.MaxAgeSeconds != 120 {
			t.Fatalf("maxAge = %d", r.MaxAgeSeconds)
		}
	})

	t.Run("PerBucketCORS_PreflightRespectsRules", func(t *testing.T) {
		bucket := uniqueBucket("parity-j")
		putBucketAdmin(t, bucket)

		body := fmt.Sprintf(`{"CORSRules":[{"AllowedMethods":["GET","PUT"],"AllowedOrigins":[%q],"AllowedHeaders":["*"]}]}`, originTrusted)
		putResp := adminDo(t, adminRequest(t, http.MethodPut, "/s3/"+bucket+"?cors",
			[]byte(body), ctJSON))
		_ = readAllClose(t, putResp)
		if putResp.StatusCode != http.StatusOK {
			t.Fatalf(fmtAdminPutCORSErr, putResp.StatusCode)
		}

		// Trusted origin preflight — unauthenticated: preflight runs before
		// SigV4 on the 9000 router.
		trusted, err := http.NewRequest(http.MethodOptions, storageURL+"/"+bucket+"/x.bin", nil)
		if err != nil {
			t.Fatalf("new trusted preflight: %v", err)
		}
		trusted.Header.Set("Origin", originTrusted)
		trusted.Header.Set(hdrACReqMethod, "PUT")
		tResp, err := http.DefaultClient.Do(trusted)
		if err != nil {
			t.Fatalf("trusted preflight: %v", err)
		}
		_ = readAllClose(t, tResp)
		if got := tResp.Header.Get(hdrACAllowOrigin); got != originTrusted {
			t.Fatalf("trusted preflight ACAO = %q; want http://trusted.example (status=%d)",
				got, tResp.StatusCode)
		}

		// Untrusted origin preflight — no ACAO, and current middleware aborts
		// with 403 on a mismatched preflight. We assert the observable
		// behaviour (no ACAO) rather than pin the status code, so a later
		// middleware change that still fails closed stays green.
		untrusted, err := http.NewRequest(http.MethodOptions, storageURL+"/"+bucket+"/x.bin", nil)
		if err != nil {
			t.Fatalf("new untrusted preflight: %v", err)
		}
		untrusted.Header.Set("Origin", "http://untrusted.example")
		untrusted.Header.Set(hdrACReqMethod, "GET")
		uResp, err := http.DefaultClient.Do(untrusted)
		if err != nil {
			t.Fatalf("untrusted preflight: %v", err)
		}
		_ = readAllClose(t, uResp)
		if got := uResp.Header.Get(hdrACAllowOrigin); got != "" {
			t.Fatalf("untrusted preflight ACAO = %q; want empty (status=%d)",
				got, uResp.StatusCode)
		}
	})

	t.Run("PerBucketCORS_DeleteFlipsPreflight", func(t *testing.T) {
		bucket := uniqueBucket("parity-k")
		putBucketAdmin(t, bucket)

		body := fmt.Sprintf(`{"CORSRules":[{"AllowedMethods":["GET"],"AllowedOrigins":[%q]}]}`, originTrusted)
		putResp := adminDo(t, adminRequest(t, http.MethodPut, "/s3/"+bucket+"?cors",
			[]byte(body), ctJSON))
		_ = readAllClose(t, putResp)
		if putResp.StatusCode != http.StatusOK {
			t.Fatalf(fmtAdminPutCORSErr, putResp.StatusCode)
		}

		// Rule is active: preflight from trusted origin is answered.
		pre1, err := http.NewRequest(http.MethodOptions, storageURL+"/"+bucket+"/y.bin", nil)
		if err != nil {
			t.Fatalf("preflight1: %v", err)
		}
		pre1.Header.Set("Origin", originTrusted)
		pre1.Header.Set(hdrACReqMethod, "GET")
		r1, err := http.DefaultClient.Do(pre1)
		if err != nil {
			t.Fatalf("preflight1 do: %v", err)
		}
		_ = readAllClose(t, r1)
		if got := r1.Header.Get(hdrACAllowOrigin); got != originTrusted {
			t.Fatalf("pre-delete ACAO = %q; want trusted (status=%d)", got, r1.StatusCode)
		}

		delResp := adminDo(t, adminRequest(t, http.MethodDelete, "/s3/"+bucket+"?cors", nil, ""))
		_ = readAllClose(t, delResp)
		if delResp.StatusCode != http.StatusNoContent {
			t.Fatalf("admin DELETE cors = %d", delResp.StatusCode)
		}

		// Rule is gone: the same preflight no longer matches.
		pre2, err := http.NewRequest(http.MethodOptions, storageURL+"/"+bucket+"/y.bin", nil)
		if err != nil {
			t.Fatalf("preflight2: %v", err)
		}
		pre2.Header.Set("Origin", originTrusted)
		pre2.Header.Set(hdrACReqMethod, "GET")
		r2, err := http.DefaultClient.Do(pre2)
		if err != nil {
			t.Fatalf("preflight2 do: %v", err)
		}
		_ = readAllClose(t, r2)
		if got := r2.Header.Get(hdrACAllowOrigin); got != "" {
			t.Fatalf("post-delete ACAO = %q; want empty (status=%d)", got, r2.StatusCode)
		}
	})
}

// parseQuery is a trivial wrapper so the test reads as intent: building
// a "?cors" query with an empty value. We avoid the ambiguity of
// url.Values{"cors": {""}} literals scattered through the file.
func parseQuery(raw string) url.Values {
	m := url.Values{}
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			m[kv] = []string{""}
			continue
		}
		m[kv[:i]] = append(m[kv[:i]], kv[i+1:])
	}
	return m
}

// reflectEq is a small local slice-equality helper to keep the test file
// self-contained (reflect.DeepEqual would force another import for a
// single use-case).
func reflectEq(a, b []string) bool {
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
