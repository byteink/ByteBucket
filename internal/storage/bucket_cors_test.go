package storage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// withTempObjectsRoot repoints ObjectsRoot to a temp dir and pre-creates the
// given bucket. Cleanup restores the original root.
func withTempObjectsRoot(t *testing.T, bucket string) string {
	t.Helper()
	dir := t.TempDir()
	orig := ObjectsRoot
	ObjectsRoot = dir
	t.Cleanup(func() { ObjectsRoot = orig })
	if err := os.MkdirAll(filepath.Join(dir, bucket), 0755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	return dir
}

func TestBucketCORS_RoundTrip(t *testing.T) {
	withTempObjectsRoot(t, "b1")

	want := &BucketCORSConfig{CORSRules: []BucketCORSRule{{
		ID:             "rule1",
		AllowedMethods: []string{"GET", "PUT"},
		AllowedOrigins: []string{"https://example.com"},
		AllowedHeaders: []string{"*"},
		ExposeHeaders:  []string{"ETag"},
		MaxAgeSeconds:  3000,
	}}}

	if err := PutBucketCORS("b1", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := GetBucketCORS("b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}

func TestBucketCORS_Overwrite(t *testing.T) {
	withTempObjectsRoot(t, "b1")

	_ = PutBucketCORS("b1", &BucketCORSConfig{CORSRules: []BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"https://old.example"},
	}}})
	updated := &BucketCORSConfig{CORSRules: []BucketCORSRule{{
		AllowedMethods: []string{"POST"},
		AllowedOrigins: []string{"https://new.example"},
	}}}
	if err := PutBucketCORS("b1", updated); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := GetBucketCORS("b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(updated, got) {
		t.Fatalf("overwrite mismatch: got %+v", got)
	}
}

func TestBucketCORS_MissingReturnsSentinel(t *testing.T) {
	withTempObjectsRoot(t, "b1")

	_, err := GetBucketCORS("b1")
	if !errors.Is(err, ErrNoSuchCORSConfiguration) {
		t.Fatalf("expected ErrNoSuchCORSConfiguration, got %v", err)
	}
}

func TestBucketCORS_DeleteMissingReturnsSentinel(t *testing.T) {
	withTempObjectsRoot(t, "b1")

	err := DeleteBucketCORS("b1")
	if !errors.Is(err, ErrNoSuchCORSConfiguration) {
		t.Fatalf("expected ErrNoSuchCORSConfiguration, got %v", err)
	}
}

func TestBucketCORS_DeleteRemovesFile(t *testing.T) {
	withTempObjectsRoot(t, "b1")

	cfg := &BucketCORSConfig{CORSRules: []BucketCORSRule{{
		AllowedMethods: []string{"GET"},
		AllowedOrigins: []string{"*"},
	}}}
	if err := PutBucketCORS("b1", cfg); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := DeleteBucketCORS("b1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := GetBucketCORS("b1"); !errors.Is(err, ErrNoSuchCORSConfiguration) {
		t.Fatalf("expected sentinel after delete, got %v", err)
	}
}
