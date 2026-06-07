package sampler

import (
	"os"
	"testing"
	"time"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"
)

func TestDelta_NormalIncrement(t *testing.T) {
	prev := middleware.RequestOutcomes{Success: 2, ClientError: 1, ServerError: 0}
	cur := middleware.RequestOutcomes{Success: 5, ClientError: 3, ServerError: 2}
	d := delta(prev, cur)
	if d.C2xx != 3 || d.C4xx != 2 || d.C5xx != 2 {
		t.Fatalf("delta = %+v, want {3,2,2}", d)
	}
}

func TestDelta_CounterResetTakesCurrent(t *testing.T) {
	// A process restart zeroes the in-memory counters, so cur < prev means the
	// current value is itself the post-reset total for this interval.
	prev := middleware.RequestOutcomes{Success: 100, ClientError: 50}
	cur := middleware.RequestOutcomes{Success: 4, ClientError: 0}
	d := delta(prev, cur)
	if d.C2xx != 4 || d.C4xx != 0 {
		t.Fatalf("reset delta = %+v, want C2xx=4 C4xx=0", d)
	}
}

func TestTick_StoresDeltaAndPrunes(t *testing.T) {
	// Isolated store via the storage package's own chdir fixture path.
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := storage.InitUserStore("users.db"); err != nil {
		t.Fatalf("init store: %v", err)
	}

	reads := []middleware.RequestOutcomes{
		{Success: 10},          // first tick: delta = 10 (prev zero)
		{Success: 13, ClientError: 1}, // second tick: delta = {3,1}
	}
	i := 0
	read := func() middleware.RequestOutcomes {
		r := reads[i]
		i++
		return r
	}
	s := &sampler{read: read}

	t0 := time.Date(2026, 6, 7, 12, 30, 30, 0, time.UTC)
	if err := s.tick(t0, 30); err != nil {
		t.Fatalf("tick0: %v", err)
	}
	// Second tick one minute later, retention generous so nothing prunes yet.
	if err := s.tick(t0.Add(time.Minute), 30); err != nil {
		t.Fatalf("tick1: %v", err)
	}

	got, err := storage.QueryRequestSamples(0, t0.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 stored samples, got %d: %+v", len(got), got)
	}
	if got[0].C2xx != 10 {
		t.Fatalf("first sample C2xx = %d, want 10", got[0].C2xx)
	}
	if got[1].C2xx != 3 || got[1].C4xx != 1 {
		t.Fatalf("second sample = %+v, want {3,1}", got[1])
	}
	// Samples must be keyed to the truncated minute, not the raw second.
	if got[0].MinuteUnix != t0.Truncate(time.Minute).Unix() {
		t.Fatalf("sample not minute-aligned: %d", got[0].MinuteUnix)
	}
}

func TestTick_RetentionPrunesOldSamples(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := storage.InitUserStore("users.db"); err != nil {
		t.Fatalf("init store: %v", err)
	}

	// Seed an ancient sample directly, then tick "now" with a 1-day retention.
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := storage.PutRequestSample(storage.RequestSample{MinuteUnix: old.Unix(), C2xx: 1}); err != nil {
		t.Fatal(err)
	}
	read := func() middleware.RequestOutcomes { return middleware.RequestOutcomes{Success: 1} }
	s := &sampler{read: read}
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if err := s.tick(now, 1); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, err := storage.QueryRequestSamples(0, now.Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	// Only the fresh sample survives; the 6-day-old one is pruned.
	if len(got) != 1 || got[0].MinuteUnix != now.Unix() {
		t.Fatalf("retention prune wrong, kept: %+v", got)
	}
}
