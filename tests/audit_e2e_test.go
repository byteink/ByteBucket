package tests

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

type auditEvent struct {
	Ts     int64  `json:"ts"`
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
}

func getAudit(t *testing.T, query string) []auditEvent {
	t.Helper()
	status, body := adminJSON(t, http.MethodGet, "/api/audit"+query, "")
	if status != http.StatusOK {
		t.Fatalf("GET audit%s: %d body %s", query, status, body)
	}
	var out struct {
		Events []auditEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode audit: %v body=%s", err, body)
	}
	return out.Events
}

func hasEvent(events []auditEvent, action, target string) bool {
	for _, e := range events {
		if e.Action == action && (target == "" || e.Target == target) {
			return true
		}
	}
	return false
}

// TestE2E_AuditLog proves control-plane mutations are recorded and queryable:
// a user creation and a config change both surface in the audit log, attributed
// to the acting admin, and the cursor/limit paging works.
func TestE2E_AuditLog(t *testing.T) {
	// A user creation must be audited as user.create against the new key.
	ak, _ := createManagedUser(t, `[{"effect":"Allow","buckets":["audit-e2e"],"actions":["*"]}]`)
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodDelete, "/api/users/"+ak, "") })

	// A config change must be audited too. Restore the default afterwards.
	if status, _ := adminJSON(t, http.MethodPut, "/api/config/retention", `{"days":21}`); status != http.StatusOK {
		t.Fatalf("set retention: %d", status)
	}
	t.Cleanup(func() { _, _ = adminJSON(t, http.MethodPut, "/api/config/retention", `{"days":30}`) })

	events := getAudit(t, "?limit=200")
	if !hasEvent(events, "user.create", ak) {
		t.Fatalf("user.create for %s not audited", ak)
	}
	if !hasEvent(events, "config.retention", "21") {
		t.Fatalf("config.retention=21 not audited")
	}
	// Events are attributed to the acting admin and carry a formatted time.
	for _, e := range events {
		if e.Action == "user.create" && e.Target == ak {
			if e.Actor != adminCreds.AccessKeyID {
				t.Fatalf("actor = %q, want %q", e.Actor, adminCreds.AccessKeyID)
			}
			if e.Time == "" || e.Ts == 0 {
				t.Fatalf("event missing time/ts: %+v", e)
			}
		}
	}
}

// TestE2E_AuditPaginationAndValidation covers the cursor paging contract and the
// malformed-cursor rejection.
func TestE2E_AuditPaginationAndValidation(t *testing.T) {
	// Generate a couple of fresh events so there is something to page through.
	ak, _ := createManagedUser(t, `[{"effect":"Allow","buckets":["audit-pg"],"actions":["*"]}]`)
	_, _ = adminJSON(t, http.MethodDelete, "/api/users/"+ak, "")

	newest := getAudit(t, "?limit=1")
	if len(newest) != 1 {
		t.Fatalf("limit=1 returned %d events", len(newest))
	}
	// Paging before the newest cursor must return strictly-older events.
	older := getAudit(t, "?before="+strconv.FormatInt(newest[0].Ts, 10))
	for _, e := range older {
		if e.Ts >= newest[0].Ts {
			t.Fatalf("before-cursor returned a non-older event: %d >= %d", e.Ts, newest[0].Ts)
		}
	}

	// A malformed cursor is a 400, not a silent ignore.
	if status, _ := adminJSON(t, http.MethodGet, "/api/audit?before=notanumber", ""); status != http.StatusBadRequest {
		t.Fatalf("bad before = %d, want 400", status)
	}
}
