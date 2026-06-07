package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// requestSeriesResp mirrors the handler's wire shape, enough to assert window
// geometry and navigation bounds.
type requestSeriesResp struct {
	Range         string `json:"range"`
	Offset        int    `json:"offset"`
	BucketSeconds int64  `json:"bucketSeconds"`
	CanBack       bool   `json:"canBack"`
	CanForward    bool   `json:"canForward"`
	Buckets       []struct {
		Ts   int64  `json:"ts"`
		C2xx uint32 `json:"c2xx"`
	} `json:"buckets"`
}

func getSeries(t *testing.T, query string) requestSeriesResp {
	t.Helper()
	resp := adminDo(t, adminRequest(t, http.MethodGet, "/api/stats/requests"+query, nil, ""))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET requests%s: %d", query, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var s requestSeriesResp
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	return s
}

// TestE2E_RequestSeries drives the navigable request-outcome chart endpoint:
// the default window geometry, the unknown-range rejection, and the nav bounds
// (no forward at offset 0; forward enabled once stepped back).
func TestE2E_RequestSeries(t *testing.T) {
	// Default range is 24h: 24 hourly buckets, sitting at "now" so forward is
	// disabled but back is allowed within the 30-day retention.
	def := getSeries(t, "")
	if def.Range != "24h" || def.BucketSeconds != 3600 || len(def.Buckets) != 24 {
		t.Fatalf("default window wrong: range=%s bucket=%d n=%d", def.Range, def.BucketSeconds, len(def.Buckets))
	}
	if def.CanForward {
		t.Fatal("offset 0 must not allow forward")
	}
	if !def.CanBack {
		t.Fatal("default must allow stepping back within retention")
	}

	// The 1h window is 60 one-minute buckets.
	hour := getSeries(t, "?range=1h")
	if hour.BucketSeconds != 60 || len(hour.Buckets) != 60 {
		t.Fatalf("1h window wrong: bucket=%d n=%d", hour.BucketSeconds, len(hour.Buckets))
	}

	// Stepping back enables forward navigation.
	back := getSeries(t, "?range=24h&offset=2")
	if back.Offset != 2 || !back.CanForward {
		t.Fatalf("offset=2 must enable forward, got offset=%d forward=%v", back.Offset, back.CanForward)
	}

	// An unknown range is a 400, not a silent default.
	resp := adminDo(t, adminRequest(t, http.MethodGet, "/api/stats/requests?range=99y", nil, ""))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown range status = %d, want 400", resp.StatusCode)
	}
}

// TestE2E_RetentionConfig drives the retention setting endpoint: it round-trips
// and clamps out-of-range values rather than rejecting them.
func TestE2E_RetentionConfig(t *testing.T) {
	read := func() int {
		resp := adminDo(t, adminRequest(t, http.MethodGet, "/api/config/retention", nil, ""))
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET retention: %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var dto struct {
			Days int `json:"days"`
		}
		if err := json.Unmarshal(body, &dto); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return dto.Days
	}
	set := func(days int) int {
		body, _ := json.Marshal(map[string]int{"days": days})
		resp := adminDo(t, adminRequest(t, http.MethodPut, "/api/config/retention", body, "application/json"))
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT retention=%d: %d", days, resp.StatusCode)
		}
		out, _ := io.ReadAll(resp.Body)
		var dto struct {
			Days int `json:"days"`
		}
		_ = json.Unmarshal(out, &dto)
		return dto.Days
	}

	original := read()
	t.Cleanup(func() { set(original) })

	if got := set(7); got != 7 {
		t.Fatalf("set 7 = %d", got)
	}
	if read() != 7 {
		t.Fatalf("retention did not persist to 7")
	}
	// Over-range clamps to the 365 ceiling.
	if got := set(99999); got != 365 {
		t.Fatalf("set 99999 clamped to %d, want 365", got)
	}
}
