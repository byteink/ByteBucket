package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withTempObjectsRootACL points the storage-package ObjectsRoot at a fresh
// temp dir for the life of the test. Suffixed to avoid colliding with the
// existing withTempObjectsRoot helper in bucket_cors_test.go, which takes a
// pre-existing bucket name.
func withTempObjectsRootACL(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := ObjectsRoot
	ObjectsRoot = dir
	t.Cleanup(func() { ObjectsRoot = orig })
	return dir
}

func TestNormalizeCannedACL(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    string
		wantErr bool
	}{
		"empty defaults to private": {in: "", want: ACLPrivate},
		"explicit private":          {in: "private", want: ACLPrivate},
		"public-read":               {in: "public-read", want: ACLPublicRead},
		"mixed case":                {in: "Public-Read", want: ACLPublicRead},
		"unsupported":               {in: "public-read-write", wantErr: true},
		"junk":                      {in: "anything", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeCannedACL(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBucketACLRoundTrip(t *testing.T) {
	dir := withTempObjectsRootACL(t)
	bucket := "b1"
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Absent sidecar reports "private" via EffectiveBucketACL but returns
	// the sentinel via GetBucketACL — the two contracts are deliberate so
	// callers can distinguish "never set" from "explicitly private".
	if _, err := GetBucketACL(bucket); !errors.Is(err, ErrNoSuchBucketACL) {
		t.Fatalf("expected ErrNoSuchBucketACL, got %v", err)
	}
	eff, err := EffectiveBucketACL(bucket)
	if err != nil || eff != ACLPrivate {
		t.Fatalf("default effective=%q err=%v", eff, err)
	}

	if err := PutBucketACL(bucket, &BucketACL{Canned: ACLPublicRead}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := EffectiveBucketACL(bucket)
	if err != nil || got != ACLPublicRead {
		t.Fatalf("after put effective=%q err=%v", got, err)
	}
}

func TestObjectACLOverridesBucket(t *testing.T) {
	dir := withTempObjectsRootACL(t)
	bucket := "b1"
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	objPath := filepath.Join(dir, bucket, "k")
	if err := os.WriteFile(objPath, []byte("body"), 0644); err != nil {
		t.Fatalf("write object: %v", err)
	}

	// Bucket public, object inherits.
	if err := PutBucketACL(bucket, &BucketACL{Canned: ACLPublicRead}); err != nil {
		t.Fatalf("put bucket acl: %v", err)
	}
	acl, src, err := ResolveObjectACL(bucket, objPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if acl != ACLPublicRead || src != ACLSourceBucket {
		t.Fatalf("inherit: acl=%q src=%q", acl, src)
	}

	// Object overrides to private.
	if err := SetObjectACL(objPath, ACLPrivate); err != nil {
		t.Fatalf("set object acl: %v", err)
	}
	acl, src, err = ResolveObjectACL(bucket, objPath)
	if err != nil {
		t.Fatalf("resolve after override: %v", err)
	}
	if acl != ACLPrivate || src != ACLSourceObject {
		t.Fatalf("override: acl=%q src=%q", acl, src)
	}
}

func TestUnsupportedCannedACLRejected(t *testing.T) {
	dir := withTempObjectsRootACL(t)
	bucket := "b1"
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := PutBucketACL(bucket, &BucketACL{Canned: "public-read-write"}); err == nil {
		t.Fatalf("expected error for unsupported canned ACL")
	}
}
