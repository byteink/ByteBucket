package handlers

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// statsDTO is the admin dashboard summary. Storage totals are computed on
// demand by walking the store; requests is the cumulative HTTP request count
// from the metrics registry.
type statsDTO struct {
	Buckets  int64   `json:"buckets"`
	Objects  int64   `json:"objects"`
	Bytes    int64   `json:"bytes"`
	Requests float64 `json:"requests"`
}

// GetStatsHandler returns the dashboard summary: bucket/object/byte totals plus
// the cumulative request count. The walk is bounded by the objects actually on
// disk and excludes sidecars so the counts match what ListObjects reports.
func GetStatsHandler(c *gin.Context) {
	buckets, objects, bytes, err := computeStorageStats()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to compute stats")
		return
	}
	c.JSON(http.StatusOK, statsDTO{
		Buckets:  buckets,
		Objects:  objects,
		Bytes:    bytes,
		Requests: middleware.TotalRequests(),
	})
}

// computeStorageStats walks objectsRoot once: each top-level directory is a
// bucket, every non-sidecar regular file beneath it is an object. A missing
// root (no buckets created yet) is reported as all-zero, not an error.
func computeStorageStats() (buckets, objects, bytes int64, err error) {
	entries, err := os.ReadDir(objectsRoot)
	if os.IsNotExist(err) {
		return 0, 0, 0, nil
	}
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		buckets++
		o, b, walkErr := walkBucketObjects(filepath.Join(objectsRoot, e.Name()))
		if walkErr != nil {
			return 0, 0, 0, walkErr
		}
		objects += o
		bytes += b
	}
	return buckets, objects, bytes, nil
}

// walkBucketObjects sums the count and byte size of non-sidecar files under a
// single bucket directory.
func walkBucketObjects(bucketPath string) (objects, bytes int64, err error) {
	err = filepath.WalkDir(bucketPath, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || isSidecar(d.Name()) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		objects++
		bytes += info.Size()
		return nil
	})
	return objects, bytes, err
}
