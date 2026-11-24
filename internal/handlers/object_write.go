package handlers

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"ByteBucket/internal/middleware"

	"github.com/goccy/go-json"
)

type stagedObject struct {
	path     string
	etag     string
	checksum string
	written  int64
	durable  bool
}

func stageObjectWrite(dstPath string, src io.Reader) (staged *stagedObject, err error) {
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	staged = &stagedObject{path: tmp.Name(), durable: syncWritesEnabled()}
	defer func() {
		err = errors.Join(err, tmp.Close())
		if err != nil {
			err = errors.Join(err, staged.discard())
			staged = nil
		}
	}()
	err = staged.read(tmp, src)
	return staged, err
}

func (s *stagedObject) read(tmp *os.File, src io.Reader) error {
	crcHasher := crc32.NewIEEE()
	md5Hasher := md5.New()
	written, err := io.Copy(io.MultiWriter(tmp, crcHasher, md5Hasher), src)
	if err != nil {
		return err
	}
	// The rename must never publish data that durability mode has not synced.
	if s.durable {
		if err := tmp.Sync(); err != nil {
			return err
		}
	}
	if err := tmp.Chmod(0644); err != nil {
		return err
	}
	var checksumBytes [4]byte
	binary.BigEndian.PutUint32(checksumBytes[:], crcHasher.Sum32())
	s.etag = formatETag(md5Hasher)
	s.checksum = base64.StdEncoding.EncodeToString(checksumBytes[:])
	s.written = written
	return nil
}

// The caller holds the same object stripe as deletion and predicate checks.
func (s *stagedObject) commit(bucketName, dstPath string, meta map[string]string) (string, int64, error) {
	if err := os.Rename(s.path, dstPath); err != nil {
		return "", 0, err
	}
	if s.durable {
		if err := fsyncDir(filepath.Dir(dstPath)); err != nil {
			return "", 0, err
		}
	}
	meta["x-amz-checksum-crc32"] = s.checksum
	meta[etagMetaKey] = s.etag
	meta["Content-Length"] = strconv.FormatInt(s.written, 10)
	metadataJSON, err := json.Marshal(meta)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(dstPath+".meta", metadataJSON, 0644); err != nil {
		return "", 0, err
	}
	middleware.ObjectsBytesTotal.WithLabelValues(bucketName).Add(float64(s.written))
	return s.etag, s.written, nil
}

func (s *stagedObject) discard() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *stagedObject) cleanup() {
	if err := s.discard(); err != nil {
		slog.Error("failed to remove staged object", "error", err)
	}
}
