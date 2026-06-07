// Package sampler periodically snapshots the cumulative S3 request-outcome
// counters into a per-minute time series, so the admin dashboard can chart
// request health over a navigable window without an external TSDB.
package sampler

import (
	"context"
	"log/slog"
	"math"
	"time"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"
)

// Reader yields the cumulative request-outcome counts. Monotonic within a
// process; resets to zero on restart.
type Reader func() middleware.RequestOutcomes

// sampler holds the previous cumulative reading so each tick can emit the
// interval delta.
type sampler struct {
	read Reader
	last middleware.RequestOutcomes
}

// delta computes the per-interval change for each class.
func delta(prev, cur middleware.RequestOutcomes) storage.RequestSample {
	return storage.RequestSample{
		C2xx: sub(prev.Success, cur.Success),
		C4xx: sub(prev.ClientError, cur.ClientError),
		C5xx: sub(prev.ServerError, cur.ServerError),
	}
}

// sub returns cur-prev as a uint32, handling a counter reset (cur < prev, i.e.
// a restart zeroed the in-memory counter) by taking cur as the interval total.
func sub(prev, cur float64) uint32 {
	if cur < prev {
		return toU32(cur)
	}
	return toU32(cur - prev)
}

func toU32(f float64) uint32 {
	if f < 0 {
		return 0
	}
	if f > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(f)
}

// tick records one interval's delta against the truncated minute and prunes
// samples older than the retention window.
func (s *sampler) tick(now time.Time, retentionDays int) error {
	cur := s.read()
	d := delta(s.last, cur)
	s.last = cur
	d.MinuteUnix = now.Truncate(time.Minute).Unix()
	if err := storage.PutRequestSample(d); err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	return storage.PruneRequestSamples(cutoff)
}

// Run samples every interval until ctx is cancelled. retentionDays is read each
// tick so a runtime change to the retention setting takes effect without a
// restart. Failures are logged, not fatal: a dropped sample must never take the
// process down.
func Run(ctx context.Context, interval time.Duration, read Reader, retentionDays func() int) {
	s := &sampler{read: read}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := s.tick(now.UTC(), retentionDays()); err != nil {
				slog.Warn("request-sample tick failed", "err", err.Error())
			}
		}
	}
}
