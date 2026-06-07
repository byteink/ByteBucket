package storage

import "testing"

func TestRequestSamples_RoundTripWithinRange(t *testing.T) {
	setupConfigStore(t)

	samples := []RequestSample{
		{MinuteUnix: 100, C2xx: 5, C4xx: 1, C5xx: 0},
		{MinuteUnix: 101, C2xx: 2, C4xx: 0, C5xx: 3},
		{MinuteUnix: 102, C2xx: 9, C4xx: 4, C5xx: 1},
	}
	for _, s := range samples {
		if err := PutRequestSample(s); err != nil {
			t.Fatalf("put %d: %v", s.MinuteUnix, err)
		}
	}

	// [101, 103) must return 101 and 102 but exclude 100.
	got, err := QueryRequestSamples(101, 103)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2: %+v", len(got), got)
	}
	if got[0].MinuteUnix != 101 || got[0].C5xx != 3 {
		t.Fatalf("first sample wrong: %+v", got[0])
	}
	if got[1].MinuteUnix != 102 || got[1].C2xx != 9 || got[1].C4xx != 4 {
		t.Fatalf("second sample wrong: %+v", got[1])
	}
}

func TestRequestSamples_OverwriteSameMinute(t *testing.T) {
	setupConfigStore(t)
	if err := PutRequestSample(RequestSample{MinuteUnix: 50, C2xx: 1}); err != nil {
		t.Fatal(err)
	}
	if err := PutRequestSample(RequestSample{MinuteUnix: 50, C2xx: 7}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryRequestSamples(50, 51)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].C2xx != 7 {
		t.Fatalf("expected single overwritten sample C2xx=7, got %+v", got)
	}
}

func TestRequestSamples_PruneDropsOlderThanCutoff(t *testing.T) {
	setupConfigStore(t)
	for _, m := range []int64{10, 20, 30, 40} {
		if err := PutRequestSample(RequestSample{MinuteUnix: m, C2xx: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneRequestSamples(30); err != nil {
		t.Fatalf("prune: %v", err)
	}
	got, err := QueryRequestSamples(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Strictly-before-30 dropped: 10 and 20 gone, 30 and 40 kept.
	if len(got) != 2 || got[0].MinuteUnix != 30 || got[1].MinuteUnix != 40 {
		t.Fatalf("prune kept wrong set: %+v", got)
	}
}

func TestRequestSamples_EmptyRangeIsEmpty(t *testing.T) {
	setupConfigStore(t)
	got, err := QueryRequestSamples(0, 100)
	if err != nil {
		t.Fatalf("query empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no samples, got %+v", got)
	}
}
