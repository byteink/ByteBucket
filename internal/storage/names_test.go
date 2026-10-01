package storage

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBucketName(t *testing.T) {
	cases := map[string]bool{
		// good
		"a":              true,
		"ab":             true,
		"my-bucket":      true,
		"product-images": true,
		"a1b2c3":         true,
		// bad
		"":                      false,
		"UPPER":                 false,
		"with.dot":              false,
		"with_underscore":       false,
		"-leading":              false,
		"trailing-":             false,
		"double--hyphen":        false,
		"with/slash":            false,
		"../etc":                false,
		"..":                    false,
		".":                     false,
		"a%00b":                 false,
		strings.Repeat("a", 64): false,
	}
	for in, ok := range cases {
		t.Run(in, func(t *testing.T) {
			err := ValidateBucketName(in)
			if ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !ok && err == nil {
				t.Fatalf("expected reject")
			}
			if !ok && !errors.Is(err, ErrInvalidBucketName) {
				t.Fatalf("wrong error sentinel: %v", err)
			}
		})
	}
}

func TestValidateObjectKey(t *testing.T) {
	cases := []struct {
		in        string
		wantOK    bool
		wantClean string
	}{
		{"file.txt", true, "file.txt"},
		{"folder/file.txt", true, "folder/file.txt"},
		{"/leading-slash.txt", true, "leading-slash.txt"},
		{"deep/nest/inner.bin", true, "deep/nest/inner.bin"},
		// rejects
		{"", false, ""},
		{"/", false, ""},
		{"../etc/passwd", false, ""},
		{"foo/../bar", false, ""},
		{"foo/./bar", false, ""},
		{"foo//bar", false, ""},
		{"foo\x00bar", false, ""},
		{".acl.json", false, ""},
		{"folder/.acl.json", false, ""},
		{".cors.json", false, ""},
		{"data.txt.meta", false, ""},
		{"folder/data.meta", false, ""},
		{".tags.json", false, ""},
		{"file.txt.tags.json", false, ""},
		{"folder/photo.jpg.tags.json", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ValidateObjectKey(tc.in)
			if tc.wantOK && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("expected reject, got clean=%q", got)
			}
			if tc.wantOK && got != tc.wantClean {
				t.Fatalf("clean=%q want %q", got, tc.wantClean)
			}
		})
	}
}

func TestSafeJoin(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "data", "objects")
	decoded, err := url.PathUnescape("..%2F..%2Fetc%2Fpasswd")
	if err != nil {
		t.Fatalf("unescape: %v", err)
	}

	good := map[string]struct {
		names []string
		want  string
	}{
		"bucket only":                       {[]string{"b"}, filepath.Join(base, "b")},
		"nested key":                        {[]string{"b", "a/b/c.txt"}, filepath.Join(base, "b", "a", "b", "c.txt")},
		"inner dot-dot stays in bucket":     {[]string{"b", "a/../c"}, filepath.Join(base, "b", "c")},
		"still-encoded name is a plain one": {[]string{"b", "..%2Fetc"}, filepath.Join(base, "b", "..%2Fetc")},
	}
	for name, tc := range good {
		t.Run(name, func(t *testing.T) {
			got, err := SafeJoin(base, tc.names...)
			if err != nil {
				t.Fatalf("SafeJoin(%q): %v", tc.names, err)
			}
			if got != tc.want {
				t.Fatalf("SafeJoin(%q)=%q want %q", tc.names, got, tc.want)
			}
		})
	}

	bad := map[string][]string{
		"empty bucket":                  {""},
		"dot bucket":                    {"."},
		"dot-dot bucket":                {".."},
		"bucket climbs out":             {"../etc"},
		"absolute bucket":               {"/etc"},
		"empty key":                     {"b", ""},
		"dot key":                       {"b", "."},
		"key climbs out of root":        {"b", "../../etc/passwd"},
		"key steps into sibling":        {"b", "../other/k"},
		"nested climb out of bucket":    {"b", "a/../../other"},
		"absolute key":                  {"b", "/etc/passwd"},
		"decoded percent-encoded climb": {"b", decoded},
	}
	for name, names := range bad {
		t.Run(name, func(t *testing.T) {
			got, err := SafeJoin(base, names...)
			if !errors.Is(err, ErrPathEscape) {
				t.Fatalf("SafeJoin(%q)=%q err=%v want ErrPathEscape", names, got, err)
			}
			if got != "" {
				t.Fatalf("SafeJoin(%q) leaked path %q on error", names, got)
			}
		})
	}
}

// TestStorageRejectsTraversal plants state one level above the roots. Without
// containment a ".." bucket or upload ID resolves onto it, so each entry point
// must refuse before touching the filesystem.
func TestStorageRejectsTraversal(t *testing.T) {
	objDir, upDir := withTempRoots(t)
	parent := filepath.Dir(objDir)
	if filepath.Dir(upDir) != parent {
		t.Fatalf("roots must be siblings: %q %q", objDir, upDir)
	}
	writeFile(t, filepath.Join(parent, ".acl.json"), `{"canned":"public-read"}`)
	writeFile(t, filepath.Join(parent, ".cors.json"), `{"CORSRules":[]}`)
	writeFile(t, filepath.Join(parent, "evil", "manifest.json"), `{"uploadId":"evil","bucket":"b","key":"k"}`)
	if err := os.MkdirAll(filepath.Join(objDir, "b"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	escape := map[string]func() error{
		"GetBucketACL":     func() error { _, err := GetBucketACL(".."); return err },
		"PutBucketACL":     func() error { return PutBucketACL("..", &BucketACL{Canned: ACLPrivate}) },
		"GetBucketCORS":    func() error { _, err := GetBucketCORS(".."); return err },
		"PutBucketCORS":    func() error { return PutBucketCORS("..", &BucketCORSConfig{}) },
		"DeleteBucketCORS": func() error { return DeleteBucketCORS("..") },
		"CreateMultipart":  func() error { _, err := CreateMultipartUpload("..", "k", nil); return err },
		"ListMultipart":    func() error { _, err := ListMultipartUploads(".."); return err },
	}
	for name, call := range escape {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("err=%v want ErrPathEscape", err)
			}
		})
	}

	noUpload := map[string]func() error{
		"GetMultipartUpload": func() error { _, err := GetMultipartUpload("b", "k", "../../evil"); return err },
		"UploadPart": func() error {
			_, err := UploadPart("b", "k", "../../evil", 1, bytes.NewReader([]byte("x")))
			return err
		},
		"ListParts":            func() error { _, err := ListParts("b", "k", "../../evil"); return err },
		"AbortMultipartUpload": func() error { return AbortMultipartUpload("b", "k", "../../evil") },
	}
	for name, call := range noUpload {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrNoSuchUpload) {
				t.Fatalf("err=%v want ErrNoSuchUpload", err)
			}
		})
	}

	if data, err := os.ReadFile(filepath.Join(parent, ".acl.json")); err != nil || string(data) != `{"canned":"public-read"}` {
		t.Fatalf("planted ACL touched: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(parent, ".cors.json")); err != nil {
		t.Fatalf("planted CORS removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "evil", "manifest.json")); err != nil {
		t.Fatalf("planted upload removed: %v", err)
	}
}

// TestCompleteMultipartRejectsKeyEscape covers the one multipart path whose key
// becomes a filesystem path: CreateMultipartUpload stores the key verbatim, so
// Complete is where a climbing key would land outside its bucket.
func TestCompleteMultipartRejectsKeyEscape(t *testing.T) {
	objDir, _ := withTempRoots(t)
	if err := os.MkdirAll(filepath.Join(objDir, "b"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := "../../escaped"
	up, err := CreateMultipartUpload("b", key, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	part, err := UploadPart("b", key, up.UploadID, 1, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	_, _, err = CompleteMultipartUpload("b", key, up.UploadID, []UploadedPart{*part})
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("complete err=%v want ErrPathEscape", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(objDir), "escaped")); !os.IsNotExist(err) {
		t.Fatalf("object written outside root: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
