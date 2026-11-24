package handlers

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedObjectReader struct{}

func (failedObjectReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestStageObjectWrite_FailureCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "object")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	staged, err := stageObjectWrite(path, failedObjectReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || staged != nil {
		t.Fatalf("stage = %v, error = %v", staged, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("original object = %q, error = %v", got, err)
	}
	assertNoStagedObjects(t, dir)
}

func TestStageObjectWrite_InvalidParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if staged, err := stageObjectWrite(filepath.Join(parent, "object"), strings.NewReader("x")); err == nil || staged != nil {
		t.Fatalf("stage = %v, error = %v", staged, err)
	}
}

func TestStageObjectWrite_ReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Error(err)
		}
	})
	if staged, err := stageObjectWrite(filepath.Join(dir, "object"), strings.NewReader("x")); err == nil || staged != nil {
		t.Fatalf("stage = %v, error = %v", staged, err)
	}
}

func TestConditionalPut_ETagFailureCleansTemp(t *testing.T) {
	withTempObjectsRoot(t)
	seedObject(t, "cbkt", "k.txt", []byte("original"))
	path := filepath.Join(objectsRoot, "cbkt", "k.txt")
	if err := os.WriteFile(path+".meta", []byte("invalid-json"), 0644); err != nil {
		t.Fatal(err)
	}
	response := putObjectWithHeaders(t, "cbkt", "k.txt", []byte("replacement"), map[string]string{"If-Match": expectedETag([]byte("original"))})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("PUT = %d, want 500: %s", response.Code, response.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("original = %q, error = %v", got, err)
	}
	assertNoStagedObjects(t, filepath.Dir(path))
}

func TestUploadObject_CommitFailureCleansTemp(t *testing.T) {
	withTempObjectsRoot(t)
	dir := filepath.Join(objectsRoot, "cbkt")
	if err := os.MkdirAll(filepath.Join(dir, "directory"), 0755); err != nil {
		t.Fatal(err)
	}
	response := putObjectWithHeaders(t, "cbkt", "directory", []byte("content"), nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("PUT = %d, want 500: %s", response.Code, response.Body.String())
	}
	assertNoStagedObjects(t, dir)
}

func TestStagedObjectRead_FileErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		durable bool
	}{
		{"write", "content", false},
		{"sync", "", true},
		{"chmod", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "stage-")
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			staged := &stagedObject{durable: tc.durable}
			if err := staged.read(file, strings.NewReader(tc.body)); err == nil {
				t.Fatal("closed staging file accepted")
			}
		})
	}
}

func TestStagedObjectCommit_Failures(t *testing.T) {
	t.Run("rename", func(t *testing.T) {
		staged := &stagedObject{path: filepath.Join(t.TempDir(), "missing")}
		if _, _, err := staged.commit("bucket", filepath.Join(t.TempDir(), "object"), map[string]string{}); err == nil {
			t.Fatal("missing staged file committed")
		}
	})
	t.Run("metadata", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "object")
		staged, err := stageObjectWrite(path, strings.NewReader("content"))
		if err != nil {
			t.Fatal(err)
		}
		defer staged.cleanup()
		if err := os.Mkdir(path+".meta", 0755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := staged.commit("bucket", path, map[string]string{}); err == nil {
			t.Fatal("metadata directory accepted as sidecar")
		}
		assertNoStagedObjects(t, dir)
	})
}

func TestStagedObjectDiscard_Failures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	staged := &stagedObject{path: dir}
	if err := staged.discard(); err == nil {
		t.Fatal("non-empty directory removed")
	}
	staged.cleanup()
	staged.path = filepath.Join(t.TempDir(), "missing")
	if err := staged.discard(); err != nil {
		t.Fatalf("missing file cleanup: %v", err)
	}
}
