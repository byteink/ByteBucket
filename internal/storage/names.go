package storage

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidBucketName is returned when a bucket name does not satisfy the
// validation rules below. Handlers translate this into the S3
// "InvalidBucketName" wire error so SDK callers see the same code AWS would
// emit for a malformed PutBucket.
var ErrInvalidBucketName = errors.New("invalid bucket name")

// ErrInvalidObjectKey is returned for object keys that fail validation
// (path traversal, NUL bytes, reserved sidecar names). Maps to S3
// "InvalidArgument" at the handler boundary — there is no AWS code for
// "reserved key name", so InvalidArgument with a descriptive message is the
// closest fit without inventing a non-AWS code.
var ErrInvalidObjectKey = errors.New("invalid object key")

// bucketNameRe matches lowercase alphanumeric "labels" joined by single
// hyphens (no leading/trailing/double hyphens). Dots are deliberately
// rejected because they create virtual-host-style edge cases (TLS wildcard
// interaction, path vs vhost addressing) we do not need today.
//
// We are looser than S3 on minimum length: S3 requires 3 chars but a
// 1-char name is no less safe and tests/dev rigs commonly use "a" or "b1".
// The maximum is enforced separately so a regex DoS via a huge input is
// impossible.
var bucketNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// bucketNameMaxLen mirrors the S3 ceiling. Names longer than this would
// also bump against most filesystems' NAME_MAX once combined with sidecar
// suffixes, so the limit is both compatibility and safety.
const bucketNameMaxLen = 63

// ValidateBucketName rejects names that would let a hostile caller escape
// the objects root (".." segments) or collide with the bucket directory
// metadata. The single allowlisted regex below is the only legal shape; if
// a name does not match, the request is rejected before any filesystem
// operation runs.
//
// Why allowlist and not blocklist: a blocklist of "../" / "..%2F" / etc.
// reliably fails as a hardening boundary — Go's filepath.Join cleans paths
// AFTER the strings reach our handler, so URL-decoded forms that look
// innocuous (`../etc`) can still cross the boundary. A strict regex
// applied to the decoded name eliminates the whole class.
func ValidateBucketName(name string) error {
	if name == "" || len(name) > bucketNameMaxLen {
		return ErrInvalidBucketName
	}
	if !bucketNameRe.MatchString(name) {
		return ErrInvalidBucketName
	}
	return nil
}

// reservedSidecarSuffix is the meta-sidecar extension. Any key that ends
// with this is rejected so a hostile caller cannot overwrite a real
// object's metadata by uploading a file at the matching path.
const reservedSidecarSuffix = ".meta"

// reservedBucketSidecars are exact file names at the bucket root that hold
// per-bucket subresources (ACL, CORS). Allowing an object PUT to land on
// one of these would let any caller with PutObject permission silently
// rewrite the bucket's ACL or CORS config — a privilege escalation we
// close at validation time, not at the storage layer.
var reservedBucketSidecars = map[string]struct{}{
	".acl.json":  {},
	".cors.json": {},
}

// ValidateObjectKey enforces the constraints that keep the on-disk layout
// honest. After cleaning the key with path-style normalisation we require:
//   - non-empty
//   - no NUL bytes (storage subsystems handle them inconsistently; the safe
//     answer is reject)
//   - no path traversal: cleaned key must not begin with ".." and the
//     cleaned form must match the input modulo a single leading slash
//   - not match a reserved sidecar name at any depth
//   - not end with the .meta suffix
//
// The cleaned form is returned so callers can use it for all downstream
// filesystem operations — accepting one shape and persisting another would
// open a separate confusion attack.
func ValidateObjectKey(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidObjectKey
	}
	if strings.ContainsRune(key, 0) {
		return "", ErrInvalidObjectKey
	}
	// Strip a single leading slash to match the wire-shape AWS accepts but
	// reject everything else that would normalise. We do this with simple
	// string ops, not filepath.Clean — Clean is OS-aware (windows backslash
	// handling) and not the validator we want for a wire-format key.
	clean := strings.TrimPrefix(key, "/")
	if clean == "" {
		return "", ErrInvalidObjectKey
	}
	// Reject any segment that is "" (double-slash), "." or "..".
	segments := strings.Split(clean, "/")
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", ErrInvalidObjectKey
		}
		if strings.HasSuffix(seg, reservedSidecarSuffix) {
			return "", ErrInvalidObjectKey
		}
		if _, bad := reservedBucketSidecars[seg]; bad {
			return "", ErrInvalidObjectKey
		}
	}
	return clean, nil
}
