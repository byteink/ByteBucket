package storage

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
)

// BucketCORSRule mirrors an AWS S3 CORSRule. JSON field names use the exact
// AWS casing so an administrator pasting a rule from the AWS console or SDK
// needs no translation layer. XML serialisation lives in the handler because
// the storage layer is format-agnostic.
type BucketCORSRule struct {
	ID             string   `json:"ID,omitempty"`
	AllowedMethods []string `json:"AllowedMethods"`
	AllowedOrigins []string `json:"AllowedOrigins"`
	AllowedHeaders []string `json:"AllowedHeaders,omitempty"`
	ExposeHeaders  []string `json:"ExposeHeaders,omitempty"`
	MaxAgeSeconds  int      `json:"MaxAgeSeconds,omitempty"`
}

// BucketCORSConfig is the root document persisted per bucket.
type BucketCORSConfig struct {
	CORSRules []BucketCORSRule `json:"CORSRules"`
}

// ErrNoSuchCORSConfiguration is returned when a bucket has no CORS
// configuration. It maps to the S3 "NoSuchCORSConfiguration" error code at
// the handler layer.
var ErrNoSuchCORSConfiguration = errors.New("no such CORS configuration")

// ObjectsRoot is where bucket directories live. Overridable in tests so they
// can exercise CORS persistence under t.TempDir without race-unsafe globals
// in hot paths.
var ObjectsRoot = "/data/objects"

// bucketCORSPath returns the on-disk location of the CORS subresource. The
// file lives inside the bucket directory as a hidden sidecar, matching the
// .meta convention used for per-object metadata. Storing it inside the
// bucket dir means deleting the bucket also drops its CORS config atomically.
func bucketCORSPath(bucket string) string {
	return filepath.Join(ObjectsRoot, bucket, ".cors.json")
}

// GetBucketCORS reads the CORS config for a bucket. Returns
// ErrNoSuchCORSConfiguration when no config has been set.
func GetBucketCORS(bucket string) (*BucketCORSConfig, error) {
	data, err := os.ReadFile(bucketCORSPath(bucket))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSuchCORSConfiguration
		}
		return nil, err
	}
	var cfg BucketCORSConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// PutBucketCORS atomically replaces the CORS config for a bucket. The bucket
// directory must already exist; creating CORS on a non-existent bucket is a
// client error surfaced by the handler, not silently by auto-creating dirs.
func PutBucketCORS(bucket string, cfg *BucketCORSConfig) error {
	if cfg == nil {
		return errors.New("nil CORS config")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write via temp + rename so a crashed write never leaves a partially
	// truncated config file that would fail to parse on the next request.
	path := bucketCORSPath(bucket)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteBucketCORS removes a bucket's CORS config. Returns
// ErrNoSuchCORSConfiguration when none exists, so callers can surface a 404
// to the client instead of a bare success that would mask state errors.
func DeleteBucketCORS(bucket string) error {
	err := os.Remove(bucketCORSPath(bucket))
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return ErrNoSuchCORSConfiguration
	}
	return err
}
