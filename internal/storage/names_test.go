package storage

import (
	"errors"
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
