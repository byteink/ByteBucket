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

// bucketSize is a single bucket's accurate on-disk byte total.
type bucketSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// bucketRow is one bucket's row in the dashboard: on-disk size plus its
// cumulative object-operation counters.
type bucketRow struct {
	Name      string  `json:"name"`
	Bytes     int64   `json:"bytes"`
	Uploads   float64 `json:"uploads"`
	Downloads float64 `json:"downloads"`
	Deletes   float64 `json:"deletes"`
}

// statsDTO is the admin dashboard summary. Storage counts come from a bounded
// walk of the objects store; activity is real S3 object operations per bucket
// (not the admin-dominated HTTP request counter).
type statsDTO struct {
	Buckets             int64                    `json:"buckets"`
	Objects             int64                    `json:"objects"`
	Bytes               int64                    `json:"bytes"`
	MultipartInProgress float64                  `json:"multipartInProgress"`
	Activity            middleware.BucketActivity `json:"activity"`
	PerBucket           []bucketRow              `json:"perBucket"`
}

// GetStatsHandler returns the dashboard summary: storage footprint plus real
// object-operation activity (uploads/downloads/deletes/bytes), both as totals
// and per current bucket, sorted by size.
func GetStatsHandler(c *gin.Context) {
	sizes, objects, err := computeStorageStats()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to compute stats")
		return
	}

	activity := middleware.S3ActivityByBucket()

	var total middleware.BucketActivity
	for _, a := range activity {
		total.Uploads += a.Uploads
		total.Downloads += a.Downloads
		total.Deletes += a.Deletes
		total.BytesIn += a.BytesIn
		total.BytesOut += a.BytesOut
	}

	var bytes int64
	rows := make([]bucketRow, 0, len(sizes))
	for _, s := range sizes {
		bytes += s.Bytes
		r := bucketRow{Name: s.Name, Bytes: s.Bytes}
		if a := activity[s.Name]; a != nil {
			r.Uploads, r.Downloads, r.Deletes = a.Uploads, a.Downloads, a.Deletes
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Bytes > rows[j].Bytes })

	c.JSON(http.StatusOK, statsDTO{
		Buckets:             int64(len(sizes)),
		Objects:             objects,
		Bytes:               bytes,
		MultipartInProgress: middleware.MultipartInProgress(),
		Activity:            total,
		PerBucket:           rows,
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
