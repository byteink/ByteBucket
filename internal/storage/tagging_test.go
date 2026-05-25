package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedTaggedObject creates a bucket with one object and returns the object's
// on-disk path. Reuses withTempObjectsRoot (bucket_cors_test.go) for the root
// isolation so tagging tests share the same fixture contract as ACL/CORS.
func seedTaggedObject(t *testing.T, bucket, key string) string {
	t.Helper()
	dir := withTempObjectsRoot(t, bucket)
	objPath := filepath.Join(dir, bucket, key)
	if err := os.WriteFile(objPath, []byte("body"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	return objPath
}

func TestObjectTags_RoundTrip(t *testing.T) {
	objPath := seedTaggedObject(t, "b1", "k")

	// Absent sidecar reports an empty set, never an error.
	got, err := GetObjectTags(objPath)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty default: tags=%v err=%v", got, err)
	}

	want := []ObjectTag{{Key: "env", Value: "prod"}, {Key: "team", Value: "core"}}
	if err := SetObjectTags(objPath, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = GetObjectTags(objPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round-trip got %v want %v", got, want)
	}

	// Replace semantics: a second Set wholly replaces the prior set.
	if err := SetObjectTags(objPath, []ObjectTag{{Key: "only", Value: "one"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = GetObjectTags(objPath)
	if len(got) != 1 || got[0].Key != "only" {
		t.Fatalf("replace did not overwrite: %v", got)
	}

	if err := DeleteObjectTags(objPath); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = GetObjectTags(objPath)
	if len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
	// Delete is idempotent.
	if err := DeleteObjectTags(objPath); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

// TestObjectTags_DoNotTouchObjectOrMeta proves tagging is independent of object
// data: the bytes and the .meta sidecar (ETag/Content-Type live there) must be
// byte-identical before and after a tag set/clear cycle.
func TestObjectTags_DoNotTouchObjectOrMeta(t *testing.T) {
	objPath := seedTaggedObject(t, "b1", "k")
	metaPath := objPath + ".meta"
	if err := os.WriteFile(metaPath, []byte(`{"etag":"abc123","content-type":"text/plain"}`), 0644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	objBefore, _ := os.ReadFile(objPath)
	metaBefore, _ := os.ReadFile(metaPath)

	if err := SetObjectTags(objPath, []ObjectTag{{Key: "k", Value: "v"}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := DeleteObjectTags(objPath); err != nil {
		t.Fatalf("delete: %v", err)
	}

	objAfter, _ := os.ReadFile(objPath)
	metaAfter, _ := os.ReadFile(metaPath)
	if string(objBefore) != string(objAfter) {
		t.Fatalf("object bytes changed by tagging")
	}
	if string(metaBefore) != string(metaAfter) {
		t.Fatalf("meta sidecar (ETag/Content-Type) changed by tagging")
	}
}

func TestValidateObjectTags(t *testing.T) {
	longKey := strings.Repeat("k", MaxTagKeyLen+1)
	longVal := strings.Repeat("v", MaxTagValueLen+1)
	tooMany := make([]ObjectTag, MaxObjectTags+1)
	for i := range tooMany {
		tooMany[i] = ObjectTag{Key: string(rune('a' + i)), Value: "v"}
	}

	cases := map[string]struct {
		tags    []ObjectTag
		wantErr bool
	}{
		"empty ok":         {tags: []ObjectTag{}},
		"max tags ok":      {tags: tooMany[:MaxObjectTags]},
		"over max tags":    {tags: tooMany, wantErr: true},
		"empty key":        {tags: []ObjectTag{{Key: "", Value: "v"}}, wantErr: true},
		"key at limit":     {tags: []ObjectTag{{Key: strings.Repeat("k", MaxTagKeyLen), Value: "v"}}},
		"key over limit":   {tags: []ObjectTag{{Key: longKey, Value: "v"}}, wantErr: true},
		"value empty ok":   {tags: []ObjectTag{{Key: "k", Value: ""}}},
		"value at limit":   {tags: []ObjectTag{{Key: "k", Value: strings.Repeat("v", MaxTagValueLen)}}},
		"value over limit": {tags: []ObjectTag{{Key: "k", Value: longVal}}, wantErr: true},
		"duplicate key":    {tags: []ObjectTag{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}}, wantErr: true},
		"distinct keys ok": {tags: []ObjectTag{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateObjectTags(tc.tags)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestSetObjectTags_RejectsInvalid proves the storage layer refuses an illegal
// set even if a handler ever forgot to validate first (defence in depth), and
// that the sidecar is never written on rejection.
func TestSetObjectTags_RejectsInvalid(t *testing.T) {
	objPath := seedTaggedObject(t, "b1", "k")
	bad := []ObjectTag{{Key: "dup", Value: "1"}, {Key: "dup", Value: "2"}}
	if err := SetObjectTags(objPath, bad); !errors.Is(err, ErrInvalidTagSet) {
		t.Fatalf("expected ErrInvalidTagSet, got %v", err)
	}
	if _, err := os.Stat(objPath + ".tags.json"); !os.IsNotExist(err) {
		t.Fatalf("sidecar written despite rejection: %v", err)
	}
}
