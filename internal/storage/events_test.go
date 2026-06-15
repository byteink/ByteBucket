package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// setupEventStore opens an isolated logs.db in a temp dir and resets the
// access-log settings to defaults, so each test starts from a known state.
func setupEventStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := InitEventStore(fmt.Sprintf("logs-%d.db", time.Now().UnixNano())); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	SetAccessLogEnabled(false)
	SetAccessLogMaxEvents(100000)
	SetAccessLogMaxAge(30 * 24 * time.Hour)
	eventsDropped.Store(0)
}

func TestControlEvents_AppendAndQueryNewestFirst(t *testing.T) {
	setupEventStore(t)
	for _, e := range []Event{
		{TimeUnixNano: 100, Category: EventControl, Actor: "admin", Op: "user.create", Target: "u1"},
		{TimeUnixNano: 200, Category: EventControl, Actor: "admin", Op: "config.sync", Target: "false"},
		{TimeUnixNano: 300, Category: EventControl, Actor: "admin", Op: "user.delete", Target: "u1"},
	} {
		if err := AppendEvent(e); err != nil {
			t.Fatalf("append %d: %v", e.TimeUnixNano, err)
		}
	}
	got, err := QueryEvents(EventControl, 10, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].TimeUnixNano != 300 || got[1].TimeUnixNano != 200 || got[2].TimeUnixNano != 100 {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].Op != "user.delete" || got[0].Actor != "admin" {
		t.Fatalf("payload wrong: %+v", got[0])
	}
}

func TestEvents_QueryLimit(t *testing.T) {
	setupEventStore(t)
	for i := int64(1); i <= 5; i++ {
		if err := AppendEvent(Event{TimeUnixNano: i * 10, Category: EventControl, Op: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := QueryEvents(EventControl, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TimeUnixNano != 50 || got[1].TimeUnixNano != 40 {
		t.Fatalf("limit wrong: %+v", got)
	}
}

func TestEvents_PaginationBefore(t *testing.T) {
	setupEventStore(t)
	for i := int64(1); i <= 5; i++ {
		if err := AppendEvent(Event{TimeUnixNano: i, Category: EventControl, Op: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := QueryEvents(EventControl, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TimeUnixNano != 2 || got[1].TimeUnixNano != 1 {
		t.Fatalf("before-cursor wrong: %+v", got)
	}
}

func TestEvents_SameNanoNoCollision(t *testing.T) {
	setupEventStore(t)
	if err := AppendEvent(Event{TimeUnixNano: 42, Category: EventControl, Op: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(Event{TimeUnixNano: 42, Category: EventControl, Op: "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryEvents(EventControl, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("same-nano events collided: got %d, want 2", len(got))
	}
}

func TestControlEvents_PruneCap(t *testing.T) {
	setupEventStore(t)
	prev := maxControlEvents
	maxControlEvents = 3
	t.Cleanup(func() { maxControlEvents = prev })

	for i := int64(1); i <= 6; i++ {
		if err := AppendEvent(Event{TimeUnixNano: i, Category: EventControl, Op: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := QueryEvents(EventControl, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].TimeUnixNano != 6 || got[2].TimeUnixNano != 4 {
		t.Fatalf("prune cap wrong: %+v", got)
	}
}

// Control and data live in separate buckets: neither category bleeds into the
// other's query, so a data flood can never evict audit history.
func TestEvents_CategoryIsolation(t *testing.T) {
	setupEventStore(t)
	if err := AppendEvent(Event{TimeUnixNano: 1, Category: EventControl, Op: "user.create"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(Event{TimeUnixNano: 2, Category: EventData, Op: "GetObject", Bucket: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	control, _ := QueryEvents(EventControl, 10, 0)
	data, _ := QueryEvents(EventData, 10, 0)
	if len(control) != 1 || control[0].Op != "user.create" {
		t.Fatalf("control bucket wrong: %+v", control)
	}
	if len(data) != 1 || data[0].Op != "GetObject" || data[0].Key != "k" {
		t.Fatalf("data bucket wrong: %+v", data)
	}
}

func TestDataEvents_BatchFlush(t *testing.T) {
	setupEventStore(t)
	SetAccessLogMaxAge(0) // synthetic epoch-tiny timestamps; isolate from the age cap
	batch := []Event{
		{TimeUnixNano: 10, Category: EventData, Op: "PutObject", Bucket: "b", Key: "a"},
		{TimeUnixNano: 20, Category: EventData, Op: "GetObject", Bucket: "b", Key: "a"},
	}
	if err := flushEvents(batch); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, _ := QueryEvents(EventData, 10, 0)
	if len(got) != 2 || got[0].TimeUnixNano != 20 {
		t.Fatalf("batch flush wrong: %+v", got)
	}
}

func TestDataEvents_PruneByCount(t *testing.T) {
	setupEventStore(t)
	SetAccessLogMaxEvents(2)
	SetAccessLogMaxAge(0) // age cap off; isolate the count cap
	for i := int64(1); i <= 5; i++ {
		if err := AppendEvent(Event{TimeUnixNano: i, Category: EventData, Op: "GetObject"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := QueryEvents(EventData, 100, 0)
	if len(got) != 2 || got[0].TimeUnixNano != 5 || got[1].TimeUnixNano != 4 {
		t.Fatalf("count prune wrong: %+v", got)
	}
}

func TestDataEvents_PruneByAge(t *testing.T) {
	setupEventStore(t)
	SetAccessLogMaxEvents(0) // count cap off; isolate the age cap
	SetAccessLogMaxAge(time.Hour)
	now := time.Now()
	old := now.Add(-2 * time.Hour).UnixNano()
	fresh := now.Add(-1 * time.Minute).UnixNano()
	for _, ts := range []int64{old, fresh} {
		if err := AppendEvent(Event{TimeUnixNano: ts, Category: EventData, Op: "GetObject"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := QueryEvents(EventData, 100, 0)
	if len(got) != 1 || got[0].TimeUnixNano != fresh {
		t.Fatalf("age prune wrong: want only the fresh event, got %+v", got)
	}
}

func TestEnqueueEvent_GatedByEnabled(t *testing.T) {
	setupEventStore(t)
	SetAccessLogEnabled(false)
	EnqueueEvent(Event{Category: EventData, Op: "GetObject"})
	if len(eventCh) != 0 {
		t.Fatalf("disabled enqueue buffered an event: len=%d", len(eventCh))
	}
	SetAccessLogEnabled(true)
	EnqueueEvent(Event{Category: EventData, Op: "GetObject"})
	if len(eventCh) != 1 {
		t.Fatalf("enabled enqueue did not buffer: len=%d", len(eventCh))
	}
}

func TestEnqueueEvent_DropsWhenFull(t *testing.T) {
	setupEventStore(t)
	SetAccessLogEnabled(true)
	// Fill the buffer, then overshoot: the surplus must be dropped and counted,
	// never block.
	for i := 0; i < eventChanCap+3; i++ {
		EnqueueEvent(Event{Category: EventData, Op: "GetObject"})
	}
	if d := eventsDropped.Load(); d != 3 {
		t.Fatalf("dropped = %d, want 3", d)
	}
}

// A cancelled context makes RunEventFlusher drain the buffer and return, so the
// final partial batch is not lost on shutdown.
func TestRunEventFlusher_DrainsOnShutdown(t *testing.T) {
	setupEventStore(t)
	SetAccessLogEnabled(true)
	SetAccessLogMaxAge(0) // synthetic epoch-tiny timestamps; isolate from the age cap
	for i := int64(1); i <= 3; i++ {
		EnqueueEvent(Event{TimeUnixNano: i, Category: EventData, Op: "GetObject"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunEventFlusher(ctx, time.Hour, 100)
	got, _ := QueryEvents(EventData, 100, 0)
	if len(got) != 3 {
		t.Fatalf("flusher drain wrote %d events, want 3", len(got))
	}
}

// A running flusher writes buffered events on the interval tick (not only on
// shutdown), exercising the live select loop.
func TestRunEventFlusher_FlushesOnTick(t *testing.T) {
	setupEventStore(t)
	SetAccessLogEnabled(true)
	SetAccessLogMaxAge(0) // synthetic timestamp; isolate from the age cap
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunEventFlusher(ctx, 10*time.Millisecond, 500)
		close(done)
	}()
	EnqueueEvent(Event{TimeUnixNano: 1, Category: EventData, Op: "GetObject"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := QueryEvents(EventData, 10, 0); len(got) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if got, _ := QueryEvents(EventData, 10, 0); len(got) != 1 {
		t.Fatalf("ticker flush wrote %d events, want 1", len(got))
	}
}

func TestAccessLogEnabled_ReflectsSetter(t *testing.T) {
	setupEventStore(t)
	SetAccessLogEnabled(true)
	if !AccessLogEnabled() {
		t.Fatal("AccessLogEnabled() = false after enabling")
	}
	SetAccessLogEnabled(false)
	if AccessLogEnabled() {
		t.Fatal("AccessLogEnabled() = true after disabling")
	}
}

func TestAccessLogSettings_Clamp(t *testing.T) {
	setupEventStore(t)
	SetAccessLogMaxEvents(-1)
	if AccessLogMaxEvents() != 0 {
		t.Fatalf("negative count cap not clamped to 0: %d", AccessLogMaxEvents())
	}
	SetAccessLogMaxAge(-time.Hour)
	if AccessLogMaxAge() != 0 {
		t.Fatalf("negative age cap not clamped to 0: %v", AccessLogMaxAge())
	}
	SetAccessLogMaxAge(48 * time.Hour)
	if AccessLogMaxAge() != 48*time.Hour {
		t.Fatalf("age round-trip wrong: %v", AccessLogMaxAge())
	}
}
