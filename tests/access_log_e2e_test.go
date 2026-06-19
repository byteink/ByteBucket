package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type logEvent struct {
	Ts       int64  `json:"ts"`
	Category string `json:"category"`
	Actor    string `json:"actor"`
	Op       string `json:"op"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Status   int    `json:"status"`
	ClientIP string `json:"clientIp"`
	Time     string `json:"time"`
}

func getLogs(t *testing.T, query string) []logEvent {
	t.Helper()
	status, body := adminJSON(t, http.MethodGet, "/api/logs"+query, "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/logs%s: %d body %s", query, status, body)
	}
	var out struct {
		Events []logEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode logs: %v body=%s", err, body)
	}
	return out.Events
}

// waitForDataEvent polls the access log until the expected data event lands. The
// flusher batches writes off the request path, so the event is eventually — not
// immediately — visible; polling closes that gap deterministically.
func waitForDataEvent(t *testing.T, op, bucket, key string) logEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range getLogs(t, "?category=data&limit=200") {
			if e.Op == op && e.Bucket == bucket && e.Key == key {
				return e
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("data event %s %s/%s not visible within deadline", op, bucket, key)
	return logEvent{}
}

func setAccessLog(t *testing.T, enabled bool) {
	t.Helper()
	body := `{"enabled":false,"maxEvents":1000,"maxAgeDays":30}`
	if enabled {
		body = `{"enabled":true,"maxEvents":1000,"maxAgeDays":30}`
	}
	if status, b := adminJSON(t, http.MethodPut, "/api/config/accesslog", body); status != http.StatusOK {
		t.Fatalf("set access log enabled=%v: %d %s", enabled, status, b)
	}
}

func setTrustedProxy(t *testing.T, body string) {
	t.Helper()
	if status, b := adminJSON(t, http.MethodPut, "/api/config/trustedproxy", body); status != http.StatusOK {
		t.Fatalf("set trusted proxy %s: %d %s", body, status, b)
	}
}

// TestE2E_TrustedProxyIPCapture proves the end-to-end vendor-header path: with
// CF-Connecting-IP trusted, an S3 request carrying that header is logged with
// the client IP it names (not the socket peer), and the whoami validation
// endpoint resolves and reports the same address with the detected header.
func TestE2E_TrustedProxyIPCapture(t *testing.T) {
	setTrustedProxy(t, `{"headers":["CF-Connecting-IP"],"useLeftmostIP":false}`)
	setAccessLog(t, true)
	t.Cleanup(func() {
		setAccessLog(t, false)
		setTrustedProxy(t, `{"headers":[],"useLeftmostIP":false}`)
	})

	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	const bucket = "trustedproxy-e2e"
	const key = "probe/ip.txt"
	if _, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("ip capture"),
	}); err != nil {
		t.Fatalf("put object: %v", err)
	}

	// A signed GET carrying the trusted vendor header. The header is unsigned
	// (not in SignedHeaders), which SigV4 permits, so it reaches the server
	// untouched and the access log must resolve the client from it.
	resp := sigV4Do(t, http.MethodGet, "/"+bucket+"/"+key, nil, map[string]string{"CF-Connecting-IP": "9.9.9.9"})
	_ = readAllClose(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed GET status %d, want 200", resp.StatusCode)
	}

	got := waitForDataEvent(t, "GetObject", bucket, key)
	if got.ClientIP != "9.9.9.9" {
		t.Fatalf("logged clientIp = %q, want 9.9.9.9 (trusted CF-Connecting-IP)", got.ClientIP)
	}

	// The whoami validation endpoint resolves the same request the same way.
	who := adminRequest(t, http.MethodGet, "/api/whoami", nil, "")
	who.Header.Set("CF-Connecting-IP", "9.9.9.9")
	wResp := adminDo(t, who)
	wBody := readAllClose(t, wResp)
	if wResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/whoami: %d body %s", wResp.StatusCode, wBody)
	}
	var w struct {
		IP             string `json:"ip"`
		DetectedHeader string `json:"detectedHeader"`
	}
	if err := json.Unmarshal(wBody, &w); err != nil {
		t.Fatalf("decode whoami: %v body=%s", err, wBody)
	}
	if w.IP != "9.9.9.9" || w.DetectedHeader != "CF-Connecting-IP" {
		t.Fatalf("whoami = %+v, want ip 9.9.9.9 via CF-Connecting-IP", w)
	}
}

// TestE2E_AccessLog_DataPlane proves real S3 object operations on :9000 are
// captured into the data-plane access log with the right identity, and surface
// through GET /api/logs?category=data on :9001.
func TestE2E_AccessLog_DataPlane(t *testing.T) {
	setAccessLog(t, true)
	t.Cleanup(func() { setAccessLog(t, false) })

	client := createS3Client(adminCreds.AccessKeyID, adminCreds.SecretAccessKey)
	const bucket = "accesslog-e2e"
	const key = "probe/object.txt"
	if _, err := client.CreateBucket(context.TODO(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("hello access log"),
	}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	resp, err := client.GetObject(context.TODO(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	get := waitForDataEvent(t, "GetObject", bucket, key)
	if get.Category != "data" || get.Status != http.StatusOK {
		t.Fatalf("GetObject event wrong: %+v", get)
	}
	if get.Actor != adminCreds.AccessKeyID {
		t.Fatalf("GetObject actor = %q, want %q", get.Actor, adminCreds.AccessKeyID)
	}
	if get.Time == "" || get.Ts == 0 {
		t.Fatalf("GetObject event missing time/ts: %+v", get)
	}
	// The write is captured too.
	_ = waitForDataEvent(t, "PutObject", bucket, key)
}

// TestE2E_AccessLog_ControlCategory proves the unified log still serves the
// control-plane trail under ?category=control (the /api/audit data, one schema).
func TestE2E_AccessLog_ControlCategory(t *testing.T) {
	// A config change is a control-plane mutation; it must appear in the trail.
	setAccessLog(t, false)
	for _, e := range getLogs(t, "?category=control&limit=200") {
		if e.Op == "config.accesslog" {
			if e.Category != "control" {
				t.Fatalf("control event mis-categorised: %+v", e)
			}
			return
		}
	}
	t.Fatalf("config.accesslog not found in control-plane log")
}

// TestE2E_AccessLog_Validation covers the required-category and malformed-cursor
// contracts on the read API.
func TestE2E_AccessLog_Validation(t *testing.T) {
	for _, q := range []string{"", "?category=bogus"} {
		if status, _ := adminJSON(t, http.MethodGet, "/api/logs"+q, ""); status != http.StatusBadRequest {
			t.Fatalf("GET /api/logs%s = %d, want 400", q, status)
		}
	}
	if status, _ := adminJSON(t, http.MethodGet, "/api/logs?category=data&before=notanumber", ""); status != http.StatusBadRequest {
		t.Fatalf("bad before = %d, want 400", status)
	}
}
