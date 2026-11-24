package tests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestE2E_ConditionalRequests drives precondition handling against the running
// container over raw HTTP so the exact status codes (304 / 412) are asserted.
// gin defers WriteHeader, so an empty 304 can regress to 200 if the handler
// stops relying on explicit flushing — this guards that.
func TestE2E_ConditionalRequests(t *testing.T) {
	const bucket = "cond-e2e-bkt"
	const key = "obj.txt"

	resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket, nil, ""))
	_ = resp.Body.Close()
	resp = adminDo(t, adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/"+key, []byte("conditional"), "text/plain"))
	etag := resp.Header.Get("ETag")
	_ = resp.Body.Close()
	if etag == "" {
		t.Fatal("PUT did not return an ETag")
	}
	t.Cleanup(func() {
		r := adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket+"/"+key, nil, ""))
		_ = r.Body.Close()
		r = adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, ""))
		_ = r.Body.Close()
	})

	// GET If-None-Match with the current ETag -> 304.
	req := adminRequest(t, http.MethodGet, "/api/s3/"+bucket+"/"+key, nil, "")
	req.Header.Set("If-None-Match", etag)
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match current ETag: got %d want 304", resp.StatusCode)
	}

	// GET If-Match with a stale ETag -> 412.
	req = adminRequest(t, http.MethodGet, "/api/s3/"+bucket+"/"+key, nil, "")
	req.Header.Set("If-Match", `"deadbeef"`)
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match stale ETag: got %d want 412", resp.StatusCode)
	}

	// PUT If-None-Match: * onto the existing key -> 412 (create-only guard).
	req = adminRequest(t, http.MethodPut, "/api/s3/"+bucket+"/"+key, []byte("v2"), "text/plain")
	req.Header.Set("If-None-Match", "*")
	resp = adminDo(t, req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match:* on existing: got %d want 412", resp.StatusCode)
	}
}

func TestE2E_ConcurrentConditionalWrites(t *testing.T) {
	for _, surface := range []string{"admin", "sigv4"} {
		for _, condition := range []string{"If-None-Match", "If-Match"} {
			t.Run(surface+"/"+condition, func(t *testing.T) {
				bucket := uniqueBucket("conditional-race")
				ensureBucket(t, adminCreds.AccessKeyID, adminCreds.SecretAccessKey, bucket)
				path := "/" + bucket + "/object.bin"
				t.Cleanup(func() {
					readAllClose(t, adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3"+path, nil, "")))
					readAllClose(t, adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, "")))
				})
				validator := "*"
				if condition == "If-Match" {
					response := adminDo(t, adminRequest(t, http.MethodPut, "/api/s3"+path, []byte("seed"), "text/plain"))
					validator = response.Header.Get("ETag")
					readAllClose(t, response)
					if response.StatusCode != http.StatusOK || validator == "" {
						t.Fatalf("seed: HTTP %d, ETag %q", response.StatusCode, validator)
					}
				}
				winner := raceConditionalWrites(t, surface, path, condition, validator)
				response := adminDo(t, adminRequest(t, http.MethodGet, "/api/s3"+path, nil, ""))
				if body := readAllClose(t, response); response.StatusCode != http.StatusOK || !bytes.Equal(body, winner) {
					t.Fatalf("stored object differs from the successful conditional write: HTTP %d", response.StatusCode)
				}
			})
		}
	}
}

type conditionalWriteResult struct {
	status  int
	payload []byte
	err     error
}

func raceConditionalWrites(t *testing.T, surface, path, condition, validator string) []byte {
	t.Helper()
	const contenders = 8
	start := make(chan struct{})
	results := make(chan conditionalWriteResult, contenders)
	client := &http.Client{Timeout: 10 * time.Second}
	for i := range contenders {
		payload := []byte(fmt.Sprintf("candidate-%d:", i) + strings.Repeat("x", 64<<10))
		req := conditionalWriteRequest(t, surface, path, payload)
		req.Header.Set(condition, validator)
		go func() {
			<-start
			result := conditionalWriteResult{payload: payload}
			response, err := client.Do(req.WithContext(t.Context()))
			if err == nil {
				result.status = response.StatusCode
				_, readErr := io.Copy(io.Discard, response.Body)
				err = errors.Join(readErr, response.Body.Close())
			}
			result.err = err
			results <- result
		}()
	}
	close(start)
	var winner []byte
	successes := 0
	for range contenders {
		result := <-results
		if result.err != nil {
			t.Errorf("conditional request: %v", result.err)
			continue
		}
		switch result.status {
		case http.StatusOK:
			successes++
			winner = result.payload
		case http.StatusPreconditionFailed:
		default:
			t.Errorf("conditional request: HTTP %d, want 200 or 412", result.status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent writes = %d, want exactly one", successes)
	}
	return winner
}

func conditionalWriteRequest(t *testing.T, surface, path string, payload []byte) *http.Request {
	t.Helper()
	if surface == "sigv4" {
		return buildHeaderSigned(t, storageURL, sigV4Request{
			method: http.MethodPut, path: path, body: payload, payloadHash: "UNSIGNED-PAYLOAD",
			accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
		})
	}
	req := adminRequest(t, http.MethodPut, "/api/s3"+path, payload, "application/octet-stream")
	req.Header.Set("X-Admin-AccessKey", adminCreds.AccessKeyID)
	req.Header.Set("X-Admin-Secret", adminCreds.SecretAccessKey)
	return req
}
