package handlers

import (
	"testing"
	"time"

	"ByteBucket/internal/storage"
)

// fixedNow is an arbitrary but stable instant used so window math is
// deterministic across runs.
var fixedNow = time.Date(2026, 6, 7, 12, 30, 40, 0, time.UTC)

// minute returns the unix-minute timestamp for a wall-clock minute offset from
// the top of fixedNow's hour, easing sample placement in tests.
func minuteAt(h, m int) int64 {
	return time.Date(2026, 6, 7, h, m, 0, 0, time.UTC).Unix()
}

func TestBuildRequestSeries_RejectsUnknownRange(t *testing.T) {
	_, err := buildRequestSeries(fixedNow, "9h", 0, 30*24*time.Hour, func(_, _ int64) ([]storage.RequestSample, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error for unknown range token")
	}
}

func TestBuildRequestSeries_HourWindowBucketsByMinute(t *testing.T) {
	var gotFrom, gotTo int64
	query := func(from, to int64) ([]storage.RequestSample, error) {
		gotFrom, gotTo = from, to
		return []storage.RequestSample{
			{MinuteUnix: minuteAt(12, 30), C2xx: 5, C4xx: 2, C5xx: 1}, // in-window, last bucket
			{MinuteUnix: minuteAt(12, 0), C2xx: 9},                    // in-window, earlier bucket
		}, nil
	}
	dto, err := buildRequestSeries(fixedNow, "1h", 0, 30*24*time.Hour, query)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if dto.BucketSeconds != 60 || len(dto.Buckets) != 60 {
		t.Fatalf("expected 60 one-minute buckets, got %d @ %ds", len(dto.Buckets), dto.BucketSeconds)
	}
	// Window ends at the boundary after now (12:31) and spans one hour back.
	if gotTo != time.Date(2026, 6, 7, 12, 31, 0, 0, time.UTC).Unix() {
		t.Fatalf("window end = %d", gotTo)
	}
	if gotFrom != gotTo-3600 {
		t.Fatalf("window start = %d, want end-3600", gotFrom)
	}
	// The 12:30 sample must occupy the bucket whose start is 12:30.
	var found bool
	for _, b := range dto.Buckets {
		if b.Ts == minuteAt(12, 30) {
			found = true
			if b.C2xx != 5 || b.C4xx != 2 || b.C5xx != 1 {
				t.Fatalf("12:30 bucket wrong: %+v", b)
			}
		}
	}
	if !found {
		t.Fatal("12:30 sample not bucketed")
	}
	if dto.Totals.C2xx != 14 || dto.Totals.C4xx != 2 || dto.Totals.C5xx != 1 {
		t.Fatalf("window totals wrong: %+v", dto.Totals)
	}
}

func TestBuildRequestSeries_OffsetShiftsWindowBack(t *testing.T) {
	var gotFrom, gotTo int64
	query := func(from, to int64) ([]storage.RequestSample, error) {
		gotFrom, gotTo = from, to
		return nil, nil
	}
	// offset 1 on a 1h range moves the whole window back one hour.
	if _, err := buildRequestSeries(fixedNow, "1h", 1, 30*24*time.Hour, query); err != nil {
		t.Fatalf("build: %v", err)
	}
	wantTo := time.Date(2026, 6, 7, 11, 31, 0, 0, time.UTC).Unix()
	if gotTo != wantTo || gotFrom != wantTo-3600 {
		t.Fatalf("offset window = [%d,%d), want end %d", gotFrom, gotTo, wantTo)
	}
}

func TestBuildRequestSeries_NavBounds(t *testing.T) {
	query := func(_, _ int64) ([]storage.RequestSample, error) { return nil, nil }

	now0, err := buildRequestSeries(fixedNow, "24h", 0, 30*24*time.Hour, query)
	if err != nil {
		t.Fatal(err)
	}
	if now0.CanForward {
		t.Fatal("offset 0 must not allow forward (no future)")
	}
	if !now0.CanBack {
		t.Fatal("30-day retention must allow stepping back from now")
	}

	back1, _ := buildRequestSeries(fixedNow, "24h", 1, 30*24*time.Hour, query)
	if !back1.CanForward {
		t.Fatal("offset 1 must allow forward")
	}

	// A 1-day retention cannot reach a window that starts before now-1d.
	tight, _ := buildRequestSeries(fixedNow, "24h", 1, 24*time.Hour, query)
	if tight.CanBack {
		t.Fatal("window already at retention edge must not allow further back")
	}
}

func TestBuildRequestSeries_NegativeOffsetClampedToZero(t *testing.T) {
	query := func(_, _ int64) ([]storage.RequestSample, error) { return nil, nil }
	dto, err := buildRequestSeries(fixedNow, "24h", -5, 30*24*time.Hour, query)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if dto.Offset != 0 || dto.CanForward {
		t.Fatalf("negative offset must clamp to 0, got offset=%d forward=%v", dto.Offset, dto.CanForward)
	}
}
