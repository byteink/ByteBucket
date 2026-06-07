package storage

import "testing"

func TestAuditLog_AppendAndQueryNewestFirst(t *testing.T) {
	setupConfigStore(t)
	for _, e := range []AuditEvent{
		{TimeUnixNano: 100, Actor: "admin", Action: "user.create", Target: "u1"},
		{TimeUnixNano: 200, Actor: "admin", Action: "config.sync", Target: "false"},
		{TimeUnixNano: 300, Actor: "admin", Action: "user.delete", Target: "u1"},
	} {
		if err := AppendAuditEvent(e); err != nil {
			t.Fatalf("append %d: %v", e.TimeUnixNano, err)
		}
	}
	got, err := QueryAuditEvents(10, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	// Newest first.
	if got[0].TimeUnixNano != 300 || got[1].TimeUnixNano != 200 || got[2].TimeUnixNano != 100 {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].Action != "user.delete" || got[0].Actor != "admin" {
		t.Fatalf("payload wrong: %+v", got[0])
	}
}

func TestAuditLog_QueryLimit(t *testing.T) {
	setupConfigStore(t)
	for i := int64(1); i <= 5; i++ {
		if err := AppendAuditEvent(AuditEvent{TimeUnixNano: i * 10, Actor: "a", Action: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := QueryAuditEvents(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TimeUnixNano != 50 || got[1].TimeUnixNano != 40 {
		t.Fatalf("limit wrong: %+v", got)
	}
}

func TestAuditLog_PaginationBefore(t *testing.T) {
	setupConfigStore(t)
	for i := int64(1); i <= 5; i++ {
		if err := AppendAuditEvent(AuditEvent{TimeUnixNano: i, Actor: "a", Action: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	// Events strictly older than ts=3 are 2 and 1, newest-first.
	got, err := QueryAuditEvents(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TimeUnixNano != 2 || got[1].TimeUnixNano != 1 {
		t.Fatalf("before-cursor wrong: %+v", got)
	}
}

func TestAuditLog_SameNanoNoCollision(t *testing.T) {
	setupConfigStore(t)
	if err := AppendAuditEvent(AuditEvent{TimeUnixNano: 42, Actor: "a", Action: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendAuditEvent(AuditEvent{TimeUnixNano: 42, Actor: "a", Action: "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryAuditEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("same-nano events collided: got %d, want 2", len(got))
	}
}

func TestAuditLog_PruneCap(t *testing.T) {
	setupConfigStore(t)
	prev := maxAuditEvents
	maxAuditEvents = 3
	t.Cleanup(func() { maxAuditEvents = prev })

	for i := int64(1); i <= 6; i++ {
		if err := AppendAuditEvent(AuditEvent{TimeUnixNano: i, Actor: "a", Action: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := QueryAuditEvents(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Only the 3 newest survive; the 3 oldest were pruned.
	if len(got) != 3 || got[0].TimeUnixNano != 6 || got[2].TimeUnixNano != 4 {
		t.Fatalf("prune cap wrong: %+v", got)
	}
}
