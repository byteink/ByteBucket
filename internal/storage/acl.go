package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-json"
)

// Canned ACL values supported by ByteBucket. Only "private" and "public-read"
// are honoured; everything else is rejected at the handler boundary to keep
// the security surface minimal in a single-tenant store.
const (
	ACLPrivate    = "private"
	ACLPublicRead = "public-read"
)

// ErrNoSuchBucketACL is returned when a bucket has no explicit ACL sidecar.
// Callers should treat absence as the implicit "private" default rather than
// surfacing this error to clients.
var ErrNoSuchBucketACL = errors.New("no such bucket ACL")

// NormalizeCannedACL validates a canned ACL string and returns the canonical
// form. Empty input maps to "private" (the S3 default for a new resource).
// Unsupported values return an error so the handler can emit InvalidArgument
// rather than silently coerce, which would surprise SDK users.
func NormalizeCannedACL(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", ACLPrivate:
		return ACLPrivate, nil
	case ACLPublicRead:
		return ACLPublicRead, nil
	}
	return "", errors.New("unsupported canned ACL")
}

// IsPublicRead reports whether a stored ACL grants anonymous read.
func IsPublicRead(acl string) bool {
	return strings.EqualFold(acl, ACLPublicRead)
}

// bucketACLPath is the on-disk location of the bucket ACL sidecar. Stored
// inside the bucket dir alongside .cors.json so DeleteBucket cleans it up
// atomically without bespoke teardown logic.
func bucketACLPath(bucket string) string {
	return filepath.Join(ObjectsRoot, bucket, ".acl.json")
}

// BucketACL is the persisted bucket-level ACL document. Kept as a single
// canned field today; expanding to per-grantee grants later only needs to
// add fields without breaking on-disk compatibility.
type BucketACL struct {
	Canned string `json:"canned"`
}

// GetBucketACL reads the ACL for a bucket. Returns ErrNoSuchBucketACL when
// no sidecar exists; callers must interpret that as "private".
func GetBucketACL(bucket string) (*BucketACL, error) {
	data, err := os.ReadFile(bucketACLPath(bucket))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSuchBucketACL
		}
		return nil, err
	}
	var acl BucketACL
	if err := json.Unmarshal(data, &acl); err != nil {
		return nil, err
	}
	return &acl, nil
}

// PutBucketACL atomically writes the ACL sidecar. The bucket directory must
// already exist; surface that failure to the caller rather than auto-creating.
func PutBucketACL(bucket string, acl *BucketACL) error {
	if acl == nil {
		return errors.New("nil bucket ACL")
	}
	canned, err := NormalizeCannedACL(acl.Canned)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(BucketACL{Canned: canned}, "", "  ")
	if err != nil {
		return err
	}
	path := bucketACLPath(bucket)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EffectiveBucketACL returns the bucket's canned ACL, defaulting to
// "private" when no sidecar exists. Read errors propagate so a corrupted
// sidecar surfaces as a 5xx instead of silently downgrading security.
func EffectiveBucketACL(bucket string) (string, error) {
	acl, err := GetBucketACL(bucket)
	if errors.Is(err, ErrNoSuchBucketACL) {
		return ACLPrivate, nil
	}
	if err != nil {
		return "", err
	}
	return acl.Canned, nil
}

// objectACLFromSidecar reads the object's .meta sidecar and returns the
// stored ACL value (lowercased canned form). Missing key or missing file
// both mean "no explicit ACL set on this object" — callers fall back to
// the bucket ACL.
func objectACLFromSidecar(objectPath string) (string, error) {
	data, err := os.ReadFile(objectPath + ".meta")
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var meta map[string]string
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	return meta["acl"], nil
}

// ACL source classifications for ResolveObjectACL. Surfaced to the UI so
// users can tell at a glance whether an object's visibility comes from its
// own override or from the bucket-level default.
const (
	ACLSourceObject  = "object"
	ACLSourceBucket  = "bucket"
	ACLSourceDefault = "default"
)

// ResolveObjectACL returns the effective canned ACL along with its source
// (object/bucket/default). Used by the admin listing to render a visibility
// column without making one API call per row.
func ResolveObjectACL(bucket, objectPath string) (acl, source string, err error) {
	obj, err := objectACLFromSidecar(objectPath)
	if err != nil {
		return "", "", err
	}
	if obj != "" {
		return strings.ToLower(obj), ACLSourceObject, nil
	}
	bucketACL, err := GetBucketACL(bucket)
	if err == nil {
		return bucketACL.Canned, ACLSourceBucket, nil
	}
	if errors.Is(err, ErrNoSuchBucketACL) {
		return ACLPrivate, ACLSourceDefault, nil
	}
	return "", "", err
}

// EffectiveObjectACL resolves the canned ACL in force for an object, applying
// the S3 precedence rule: object-level wins, otherwise inherit the bucket
// ACL, otherwise private. The objectPath is the absolute file path on disk;
// the bucket name is needed for the inheritance lookup. Both arguments come
// from the same handler that already validated the request, so we do not
// re-derive them here.
func EffectiveObjectACL(bucket, objectPath string) (string, error) {
	obj, err := objectACLFromSidecar(objectPath)
	if err != nil {
		return "", err
	}
	if obj != "" {
		return strings.ToLower(obj), nil
	}
	return EffectiveBucketACL(bucket)
}

// SetObjectACL writes the canned ACL into the object's .meta sidecar,
// preserving every other key. The object must exist; absence surfaces as
// ErrNoSuchKey-style at the handler boundary.
func SetObjectACL(objectPath, canned string) error {
	canned, err := NormalizeCannedACL(canned)
	if err != nil {
		return err
	}
	metaPath := objectPath + ".meta"

	var meta map[string]string
	if data, err := os.ReadFile(metaPath); err == nil {
		if err := json.Unmarshal(data, &meta); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if meta == nil {
		meta = make(map[string]string, 1)
	}
	meta["acl"] = canned

	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := metaPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath)
}
