# Design: unified event log (access + audit) — consolidate observability

Status: APPROVED (2026-06-15) — ready to build. Supersedes the standalone "object
access log" idea and folds the existing control-plane audit log into one event store.
Open decisions are now locked (see Decisions section).

## Problem

Observability has sprawled into five overlapping surfaces, and we were about to add
a sixth (a file-based access log) — a second persistence + retention + UI stack for
a concept we already have one of (audit). That is the over-complication to kill.

Current surfaces:

1. stdout HTTP log (`internal/middleware/log.go`) — one structured slog line per
   request, every surface. Ephemeral, platform-collected. Ops/debug.
2. Prometheus `/metrics` — aggregate counters/histograms. Health/alerting.
3. RequestSamples (BoltDB, `request_samples.go`) — per-minute 2xx/4xx/5xx, feeds the
   dashboard chart. A small self-hosted time series (deliberate "no Grafana" choice).
4. Audit log (BoltDB, `audit.go` + `GET /api/audit`) — control-plane mutations
   (user CRUD, config). Queryable, paginated UI page.
5. (gap) No per-object access record. The audit scope note says so explicitly:
   "audits the control-plane, NOT every object PUT/DELETE."

The real insight: **#4 (audit) and the proposed access log are the same concept** —
a stream of structured events, persisted, retained, viewable in the UI, queryable.
They differ only in `category` (control vs data) and volume. Build it once.

## Decision: two pillars, not six surfaces

Map everything onto the standard observability split:

- **Metrics pillar (numbers — "is it healthy")**: Prometheus + RequestSamples. The
  dashboard chart is a metrics concern; pre-aggregated per-minute samples are lower
  overhead for a chart than re-parsing logs. UNCHANGED.
- **Events pillar (records — "what happened, who did it")**: ONE BoltDB event log
  covering both control-plane (audit) and data-plane (access) as categories, plus
  the same events tee'd to stdout for ops/recent-grep. This replaces #4 and fills #5.

Net: 5 surfaces → 2 pillars; 2 planned event stores → 1.

## The events pillar

### One Event schema

```go
type Event struct {
    TimeUnixNano int64  `json:"ts"`
    Category     string `json:"category"`     // "control" | "data"
    Actor        string `json:"actor"`        // access key id; "anonymous" for anon reads
    Op           string `json:"op"`           // GetObject, PutObject, user.create, config.sync ...
    Bucket       string `json:"bucket,omitempty"`
    Key          string `json:"key,omitempty"`
    Status       int    `json:"status,omitempty"`      // HTTP status (data events)
    ErrorCode    string `json:"error_code,omitempty"`  // S3 error code (NoSuchKey ...)
    ClientIP     string `json:"client_ip,omitempty"`
    BytesIn      int64  `json:"bytes_in,omitempty"`
    BytesOut     int64  `json:"bytes_out,omitempty"`
    DurationMs   float64 `json:"duration_ms,omitempty"`
    UserAgent    string `json:"user_agent,omitempty"`
    Detail       string `json:"detail,omitempty"`      // control events: short note
}
```

`category=control` is the existing `AuditEvent` superset (Actor/Op/Detail map to
Actor/Action/Detail). `category=data` is the new access record. One struct, one
store, one read path.

### Storage: BoltDB, batched async writes, own file

- **Reuse the audit pattern verbatim**: 16-byte key (8-byte unix-nano + 8-byte
  `NextSequence`), JSON value, newest-first cursor query with a `before` cursor,
  count-cap prune. The code already exists in `audit.go`/`request_samples.go`;
  generalize it, don't reinvent it.
- **Batched writes are non-negotiable.** A BoltDB write txn fsyncs and there is a
  single writer; one-txn-per-object-GET would serialize all traffic behind fsync.
  The request path pushes the Event onto a buffered channel; a background flusher
  drains it and writes **N events per txn** (flush on `max(200ms, 500 events)`).
  One fsync amortizes many events; the hot path never touches disk or the DB lock.
  This is the make-or-break for overhead.
- **Own file: `logs.db`, NOT `users.db`.** This is a deliberate exception to the
  existing "one file to back up" rationale (see `storage.go` InitUserStore comment).
  Justification: (a) the data-plane firehose's growth must not bloat the auth
  source-of-truth; (b) BoltDB does not shrink on delete — pruned pages are reused but
  the file stays at its high-water-mark — so the log file needs independent
  compaction/rotation without touching `users.db`. TRADEOFF: a second backup target.
  Batched writes keep the writer near-idle, so lock contention is not the driver here;
  growth isolation is.

### Retention

- Count cap (like audit's `maxAuditEvents`) AND/OR age cap, configurable. Prune on
  flush, same cursor-delete as `pruneAudit`.
- **Compaction**: because the file never shrinks on delete, schedule periodic
  `bolt.Compact` (or open-copy-swap) of `logs.db` so disk tracks live data, not the
  busiest week ever. Bounded, off the hot path.
- Config keys (env + persisted Settings override, like SYNC_WRITES / retention):
  `ACCESS_LOG_ENABLED`, `ACCESS_LOG_MAX_EVENTS` (or size), `ACCESS_LOG_MAX_AGE`.
  Data-plane logging must be switchable off entirely for the cost-sensitive operator.

### stdout tee

The same structured event also goes to slog/stdout so the platform (k3s/Docker)
collects it for live tailing and recent grep. One emit point, two sinks: the BoltDB
store (durable, UI-queryable, retained) and stdout (ephemeral, ops, recent grep).
The existing `log.go` http_request line stays for full-surface ops; the events tee is
the data-plane/control subset enriched with op/bucket/key/error_code.

### Read API + UI

- `GET /api/logs?category=data|control&limit=&before=` — newest-first, paginated,
  admin-auth. Generalizes the existing `audit.go` handler.
- `GET /api/audit` stays as a thin alias for `category=control` (no UI break).
- UI: one "Logs" area with a category toggle (Control / Access). Browse + load-more,
  same UX as today's AuditPage. NO rich server-side filtering (see non-goals).

### AI / ssh investigation

Two reuse-only paths, no new surface:

- Recent: read container logs — `kubectl logs deploy/bytebucket | jq 'select(.op=="GetObject")'`.
- Historical: curl the read API on the box —
  `ssh byteink.main 'curl -s localhost:9001/api/logs?category=data\&limit=200 -H "X-Admin-AccessKey: …" -H "X-Admin-Secret: …"' | jq`.

BoltDB is not greppable directly (binary B+tree); these two cover live + historical.

## Overhead

Hot path adds: build the Event struct + a non-blocking channel send. No disk, no DB
lock, no fsync on the request. The flusher does a few txns/sec regardless of request
rate, so write amplification is bounded and decoupled from traffic. When
`ACCESS_LOG_ENABLED=false`, the data-plane path is skipped entirely.

## Migration

- Generalize `audit.go` → `events.go`: `AppendEvent(Event)`, `QueryEvents(category,
  limit, before)`, shared key/prune helpers. Keep `AppendAuditEvent`/`QueryAuditEvents`
  as thin `category=control` wrappers, OR rewrite call sites — decide at impl time.
- New bucket `EventLog` in a new `logs.db` (new `InitEventStore`). Existing `AuditLog`
  in `users.db`: small; either leave historical rows readable via a back-compat query
  or one-time copy into `logs.db`. Recommend: leave old rows, start the unified store
  fresh — audit history is short and low-stakes.

## Non-goals (keep it simple)

- No rich server-side filtering/search (by key/status/date range). Browse + load-more
  only. Confirmed not a priority. An index earns its keep later or never.
- No file/JSON-Lines sink, no lumberjack. stdout already gives the greppable stream;
  the durable+UI store is BoltDB. One store, not two.
- No S3-style server access logging to a target bucket. Out of scope.
- No tracing pillar. Request IDs already correlate.

## Security

- Never log the query string (SigV4 presigned signature lives there) or
  Authorization/credential headers. Same rule `log.go` already enforces.
- Read API is admin-auth only, same as `/api/audit`. Events may contain access key
  IDs and IPs — they must not leak to the S3 surface or anon callers.

## Testing (per CLAUDE.md TDD + pentest policy)

- Unit: AppendEvent/QueryEvents round-trip, prune-at-cap, before-cursor pagination,
  category filter, batched-flush correctness (N events one txn), enabled=false skips.
- Security/negative: read API rejects non-admin; no query-string/secret ever stored;
  hostile key in a data event is stored as data, never executed as a path.
- E2E (`tests/`): drive real object GET/PUT/DELETE on :9000, then assert the events
  show up via `GET /api/logs?category=data` on :9001 (cross-surface).
- Pentest probe (`scripts/pentest/probes.sh`): `/api/logs` requires admin (negative),
  valid admin read works (positive regression).

## Decisions (locked 2026-06-15)

1. **Names**: bucket `EventLog` in its own `logs.db`.
2. **Retention**: count cap AND age cap (both enforced on flush). Size cap rejected —
   BoltDB file size != live bytes, so a byte budget is misleading. Config keys:
   `ACCESS_LOG_MAX_EVENTS` (count) + `ACCESS_LOG_MAX_AGE` (duration), env + Settings
   override. Compaction still scheduled so the file tracks live data after prunes.
3. **Fold audit now**: one unified `EventLog`. Control-plane writes go through the
   new `AppendEvent` with `category=control`; `GET /api/audit` stays as a thin
   `category=control` alias so the existing AuditPage keeps working. Old `AuditLog`
   rows in `users.db`: leave them (short, low-stakes history), start the unified
   store fresh in `logs.db`. Once shipped, `audit.go` is retired in favor of
   `events.go`.
