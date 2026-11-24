package tests

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2E_HeaderAuthRejectsBeforeReadingBody(t *testing.T) {
	for _, unknownKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown-key-%t", unknownKey), func(t *testing.T) {
			accessKey := adminCreds.AccessKeyID
			if unknownKey {
				accessKey = "unknown-payload-user"
			}
			req := buildHeaderSigned(t, storageURL, sigV4Request{
				method: http.MethodPut, path: "/payload-auth/unread.bin",
				body: []byte("not sent"), accessKey: accessKey, secret: "wrong-secret",
			})
			conn := payloadRequestConnection(t, req)
			// No body follows: a 100 response proves authentication tried to read it.
			req.Header.Set("Content-Length", "1073741824")
			req.Header.Set("Expect", "100-continue")
			req.Header.Set("Connection", "close")
			writePayloadHeaders(t, conn, req)
			resp, err := http.ReadResponse(bufio.NewReader(conn), req)
			if err != nil {
				t.Fatalf("read rejection without sending body: %v", err)
			}
			body := readAllClose(t, resp)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("before body: HTTP %d, want 401; body %q", resp.StatusCode, body)
			}
		})
	}
}

func payloadRequestConnection(t *testing.T, req *http.Request) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", req.URL.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close request connection: %v", err)
		}
	})
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func writePayloadHeaders(t *testing.T, conn net.Conn, req *http.Request) {
	t.Helper()
	if _, err := fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: %s\r\n", req.URL.RequestURI(), req.Host); err != nil {
		t.Fatal(err)
	}
	if err := req.Header.Write(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "\r\n"); err != nil {
		t.Fatal(err)
	}
}

func TestE2E_HeaderAuthVerifiedPayloadPreservesObject(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Second}
	bucket := uniqueBucket("verified-payload")
	ensureBucket(t, adminCreds.AccessKeyID, adminCreds.SecretAccessKey, bucket)
	path := "/" + bucket + "/object.bin"
	t.Cleanup(func() {
		readAllClose(t, adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3"+path, nil, "")))
		readAllClose(t, adminDo(t, adminRequest(t, http.MethodDelete, "/api/s3/"+bucket, nil, "")))
	})
	payload := bytes.Repeat([]byte("verified-payload"), 1<<16)
	req := buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPut, path: path, body: payload,
		accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	response, err := client.Do(req.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	body := readAllClose(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("verified upload: HTTP %d, body %q", response.StatusCode, body)
	}
	req = buildHeaderSigned(t, storageURL, sigV4Request{
		method: http.MethodPut, path: path, body: []byte("tampered"),
		payloadHash: sha256Hex(payload), accessKey: adminCreds.AccessKeyID, secret: adminCreds.SecretAccessKey,
	})
	response, err = client.Do(req.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	body = readAllClose(t, response)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "XAmzContentSHA256Mismatch") {
		t.Fatalf("tampered upload: HTTP %d, body %q", response.StatusCode, body)
	}
	response = adminDo(t, adminRequest(t, http.MethodGet, "/api/s3"+path, nil, ""))
	if body := readAllClose(t, response); !bytes.Equal(body, payload) {
		t.Fatal("tampered upload replaced the verified object")
	}
}
