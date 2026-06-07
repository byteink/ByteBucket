package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// rangeGeometry is one selectable window's bucket size and bar count. The
// window duration is bucket*count; shifting the window by exactly that on each
// offset step keeps bucket boundaries wall-aligned.
type rangeGeometry struct {
	bucket time.Duration
	count  int
}

// requestRanges enumerates the dashboard's selectable windows. Bar counts are
// kept readable (<=60) at every range so the chart never has to render the
// ~43k raw minute samples a 30-day window stores.
var requestRanges = map[string]rangeGeometry{
	"1h":  {time.Minute, 60},
	"24h": {time.Hour, 24},
	"7d":  {24 * time.Hour, 7},
	"14d": {24 * time.Hour, 14},
	"30d": {24 * time.Hour, 30},
}

const defaultRequestRange = "24h"

type requestBucketDTO struct {
	Ts   int64  `json:"ts"` // bucket start, unix seconds
	C2xx uint32 `json:"c2xx"`
	C4xx uint32 `json:"c4xx"`
	C5xx uint32 `json:"c5xx"`
}

type requestTotals struct {
	C2xx uint64 `json:"c2xx"`
	C4xx uint64 `json:"c4xx"`
	C5xx uint64 `json:"c5xx"`
}

// requestSeriesDTO is the wire shape for the dashboard request chart: an
// aggregated, navigable window of S3 request outcomes.
type requestSeriesDTO struct {
	Range         string             `json:"range"`
	Offset        int                `json:"offset"`
	From          int64              `json:"from"`
	To            int64              `json:"to"`
	BucketSeconds int64              `json:"bucketSeconds"`
	CanBack       bool               `json:"canBack"`
	CanForward    bool               `json:"canForward"`
	Totals        requestTotals      `json:"totals"`
	Buckets       []requestBucketDTO `json:"buckets"`
}

// buildRequestSeries aggregates per-minute samples into a fixed grid of buckets
// for the requested window and offset. offset 0 is the window ending at now;
// each step back shifts the whole window one window-width earlier. retention
// bounds how far back navigation may go. query is injected so the math is
// testable without a store.
func buildRequestSeries(
	now time.Time,
	token string,
	offset int,
	retention time.Duration,
	query func(fromUnix, toUnix int64) ([]storage.RequestSample, error),
) (requestSeriesDTO, error) {
	geom, ok := requestRanges[token]
	if !ok {
		return requestSeriesDTO{}, fmt.Errorf("unknown range %q", token)
	}
	if offset < 0 {
		offset = 0
	}

	window := geom.bucket * time.Duration(geom.count)
	// End at the boundary after now so the in-progress bucket is the last bar.
	end := now.Truncate(geom.bucket).Add(geom.bucket).Add(-time.Duration(offset) * window)
	start := end.Add(-window)

	samples, err := query(start.Unix(), end.Unix())
	if err != nil {
		return requestSeriesDTO{}, err
	}

	bucketSecs := int64(geom.bucket / time.Second)
	startUnix := start.Unix()
	buckets := make([]requestBucketDTO, geom.count)
	for i := range buckets {
		buckets[i].Ts = startUnix + int64(i)*bucketSecs
	}

	var totals requestTotals
	for _, s := range samples {
		idx := (s.MinuteUnix - startUnix) / bucketSecs
		if idx < 0 || idx >= int64(geom.count) {
			continue // defensive: a sample outside the queried window
		}
		buckets[idx].C2xx += s.C2xx
		buckets[idx].C4xx += s.C4xx
		buckets[idx].C5xx += s.C5xx
		totals.C2xx += uint64(s.C2xx)
		totals.C4xx += uint64(s.C4xx)
		totals.C5xx += uint64(s.C5xx)
	}

	return requestSeriesDTO{
		Range:         token,
		Offset:        offset,
		From:          startUnix,
		To:            end.Unix(),
		BucketSeconds: bucketSecs,
		CanForward:    offset > 0,
		CanBack:       start.After(now.Add(-retention)),
		Totals:        totals,
		Buckets:       buckets,
	}, nil
}

// GetRequestSeriesHandler serves the navigable request-outcome chart data for
// the admin dashboard.
func GetRequestSeriesHandler(c *gin.Context) {
	token := c.DefaultQuery("range", defaultRequestRange)
	offset := 0
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			respondError(c, http.StatusBadRequest, "InvalidArgument", "offset must be an integer")
			return
		}
		offset = n
	}

	dto, err := buildRequestSeries(
		time.Now().UTC(), token, offset, requestRetentionWindow(), storage.QueryRequestSamples,
	)
	if err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Unknown range; use 1h, 24h, 7d, 14d, or 30d")
		return
	}
	c.JSON(http.StatusOK, dto)
}
