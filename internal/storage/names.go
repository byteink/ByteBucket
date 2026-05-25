package storage

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
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

// reservedSidecarSuffixes are the per-object sidecar extensions. Any key
// ending with one is rejected so a hostile caller cannot overwrite a real
// object's metadata (.meta) or tag set (.tags.json) by uploading a file at
// the matching path. These are suffixes, not exact names, because an object
// "photo.jpg" stores its sidecars as "photo.jpg.meta" / "photo.jpg.tags.json".
var reservedSidecarSuffixes = []string{".meta", ".tags.json"}

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
//   - not end with a reserved sidecar suffix (.meta, .tags.json)
//
// The cleaned form is returned so callers can use it for all downstream
// filesystem operations — accepting one shape and persisting another would
// open a separate confusion attack.
func ValidateObjectKey(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidObjectKey
	}
	// Reject any C0 control byte (0x00-0x1F) or DEL (0x7F). These would
	// otherwise survive URL decoding and end up in response headers /
	// logs / filesystem paths. AWS S3 documents that object keys may
	// "contain any sequence of Unicode characters" but the same docs
	// recommend avoiding control characters; we reject them outright
	// because they have zero legitimate use and high abuse potential
	// (response splitting, log injection, filesystem oddities).
	for _, b := range []byte(key) {
		if b < 0x20 || b == 0x7F {
			return "", ErrInvalidObjectKey
		}
	}
	// Reject invalid UTF-8. Overlong encodings (e.g. \xC0\xAF for "/")
	// historically bypassed path filters that decode AFTER validation.
	// Rejecting at the boundary closes that whole class — every legal
	// key in 2026 is well-formed UTF-8.
	if !utf8.ValidString(key) {
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
	// Reject any segment that is "" (double-slash), "." or "..", or that
	// collides with a reserved sidecar name/suffix at any depth.
	for _, seg := range strings.Split(clean, "/") {
		if !segmentIsSafe(seg) {
			return "", ErrInvalidObjectKey
		}
	}
	return clean, nil
}

// segmentIsSafe reports whether a single path segment is a legal object-key
// component. It rejects empty/relative segments and any name that would land
// on a reserved sidecar (exact bucket sidecars or per-object suffixes), so a
// hostile key cannot clobber ACL/CORS/meta/tags state at any nesting depth.
func segmentIsSafe(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	if _, bad := reservedBucketSidecars[seg]; bad {
		return false
	}
	for _, suffix := range reservedSidecarSuffixes {
		if strings.HasSuffix(seg, suffix) {
			return false
		}
	}
	return true
}
