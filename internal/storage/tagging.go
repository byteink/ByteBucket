package storage

import (
	"errors"
	"os"
	"unicode/utf8"

	"github.com/goccy/go-json"
)

// Object tagging limits mirror the AWS S3 contract. They are enforced here at
// the storage boundary as a defence in depth: even if a handler ever forgot to
// validate, a malformed tag set could never reach disk. The handler still
// validates first so it can map each violation to the precise S3 error code.
const (
	// MaxObjectTags is the AWS ceiling on tags per object.
	MaxObjectTags = 10
	// MaxTagKeyLen / MaxTagValueLen are counted in UTF-8 code points, not
	// bytes, matching how AWS documents the limits.
	MaxTagKeyLen   = 128
	MaxTagValueLen = 256
)

// ErrInvalidTagSet is returned when a tag set violates an AWS limit (count,
// key/value length, empty key, or duplicate key). Handlers translate it into
// the S3 "InvalidTag" wire error.
var ErrInvalidTagSet = errors.New("invalid tag set")

// ObjectTag is a single key/value pair. Field names match the storage-layer
// JSON sidecar; the XML wire shape lives in the handler so this layer stays
// format-agnostic, exactly like the CORS/ACL split.
type ObjectTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// objectTagsPath is the on-disk location of an object's tag sidecar. It sits
// next to the object bytes as ".tags.json", mirroring the ".meta" convention
// so a bucket delete removes it atomically with the object tree.
func objectTagsPath(objectPath string) string {
	return objectPath + ".tags.json"
}

// ValidateObjectTags enforces the AWS limits on a decoded tag set. It is the
// single source of truth for what a legal tag set is, so both the handler and
// any future caller cannot drift. Order of checks is widest-blast-radius first
// (count) so an oversized set is rejected before we iterate it.
func ValidateObjectTags(tags []ObjectTag) error {
	if len(tags) > MaxObjectTags {
		return ErrInvalidTagSet
	}
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		kl := utf8.RuneCountInString(t.Key)
		if kl < 1 || kl > MaxTagKeyLen {
			return ErrInvalidTagSet
		}
		if utf8.RuneCountInString(t.Value) > MaxTagValueLen {
			return ErrInvalidTagSet
		}
		if !utf8.ValidString(t.Key) || !utf8.ValidString(t.Value) {
			return ErrInvalidTagSet
		}
		if _, dup := seen[t.Key]; dup {
			return ErrInvalidTagSet
		}
		seen[t.Key] = struct{}{}
	}
	return nil
}

// GetObjectTags reads an object's tag set. A missing sidecar is the normal
// "no tags" state and returns an empty slice with no error, so callers never
// special-case absence. A corrupt sidecar surfaces as an error rather than a
// silent empty set, so persistence damage is visible instead of masked.
func GetObjectTags(objectPath string) ([]ObjectTag, error) {
	data, err := os.ReadFile(objectTagsPath(objectPath))
	if err != nil {
		if os.IsNotExist(err) {
			return []ObjectTag{}, nil
		}
		return nil, err
	}
	var tags []ObjectTag
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// SetObjectTags atomically replaces an object's full tag set. The tag set is
// re-validated here so a programming error elsewhere cannot persist an illegal
// set. Writing only the ".tags.json" sidecar guarantees the object bytes and
// ".meta" (ETag, Content-Type) are untouched — tagging never mutates data.
func SetObjectTags(objectPath string, tags []ObjectTag) error {
	if err := ValidateObjectTags(tags); err != nil {
		return err
	}
	if tags == nil {
		tags = []ObjectTag{}
	}
	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return err
	}
	path := objectTagsPath(objectPath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteObjectTags removes an object's tag sidecar. A missing sidecar is not
// an error: S3 DeleteObjectTagging is idempotent and returns 204 whether or
// not tags existed, so we mirror that by treating absence as success.
func DeleteObjectTags(objectPath string) error {
	err := os.Remove(objectTagsPath(objectPath))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}
