package handlers

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"ByteBucket/internal/middleware"

	"github.com/gin-gonic/gin"
)

// topBucketsLimit caps how many buckets the dashboard's "largest buckets" list
// shows, keeping the panel readable regardless of bucket count.
const topBucketsLimit = 5

// bucketSize is a single bucket's accurate on-disk byte total.
type bucketSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// statsDTO is the admin dashboard summary. Storage totals are computed on
// demand by walking the store; the request/latency/multipart figures come from
// the metrics registry.
type statsDTO struct {
	Buckets             int64              `json:"buckets"`
	Objects             int64              `json:"objects"`
	Bytes               int64              `json:"bytes"`
	Requests            float64            `json:"requests"`
	StatusClasses       map[string]float64 `json:"statusClasses"`
	MultipartInProgress float64            `json:"multipartInProgress"`
	P95LatencyMs        float64            `json:"p95LatencyMs"`
	TopBuckets          []bucketSize       `json:"topBuckets"`
}

// GetStatsHandler returns the dashboard summary. Storage counts come from a
// bounded walk of the objects store (excluding sidecars, so they match
// ListObjects); request health, p95 latency and multipart activity come from
// the metrics registry.
func GetStatsHandler(c *gin.Context) {
	perBucket, objects, err := computeStorageStats()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to compute stats")
		return
	}

	bucketCount := int64(len(perBucket))
	var bytes int64
	for _, b := range perBucket {
		bytes += b.Bytes
	}
	sort.Slice(perBucket, func(i, j int) bool { return perBucket[i].Bytes > perBucket[j].Bytes })
	if len(perBucket) > topBucketsLimit {
		perBucket = perBucket[:topBucketsLimit]
	}

	c.JSON(http.StatusOK, statsDTO{
		Buckets:             bucketCount,
		Objects:             objects,
		Bytes:               bytes,
		Requests:            middleware.TotalRequests(),
		StatusClasses:       middleware.RequestsByStatusClass(),
		MultipartInProgress: middleware.MultipartInProgress(),
		P95LatencyMs:        middleware.RequestLatencyP95Seconds() * 1000,
		TopBuckets:          perBucket,
	})
}

// computeStorageStats walks objectsRoot once: each top-level directory is a
// bucket, every non-sidecar regular file beneath it is an object. Returns the
// per-bucket byte totals and the overall object count. A missing root (no
// buckets yet) yields empty results, not an error.
func computeStorageStats() (perBucket []bucketSize, objects int64, err error) {
	entries, err := os.ReadDir(objectsRoot)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		o, b, walkErr := walkBucketObjects(filepath.Join(objectsRoot, e.Name()))
		if walkErr != nil {
			return nil, 0, walkErr
		}
		objects += o
		perBucket = append(perBucket, bucketSize{Name: e.Name(), Bytes: b})
	}
	return perBucket, objects, nil
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
