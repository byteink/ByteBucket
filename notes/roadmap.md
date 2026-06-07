# ByteBucket Roadmap

Living plan for what to build next. Ordered by impact and by how much it unblocks
real S3 clients (aws-cli, rclone, mc, SDKs). Build on this — keep it current as
items land.

## Current state (baseline)

Implemented and working:

- SigV4 surface (`:9000`) + admin surface (`:9001`, JSON + embedded React SPA)
- PutObject / GetObject / DeleteObject / HeadObject
- Multipart upload (initiate / upload part / list parts / complete / abort)
- ListObjectsV2 (KeyCount, continuation tokens, sidecar filtering)
- ACLs (`private` | `public-read`), object + bucket level, anonymous read
- Bucket CORS (`?cors`)
- Object tagging (`?tagging` put/get/delete)
- HTTP range requests (206 / 416, Accept-Ranges)
- Opt-in proxy-aware rate limiting (+ runtime config via admin UI/API)
- Presigned download URLs (`?presign`, admin-only convenience)

## Phase 0 — close testing gaps (BLOCKS everything else)

Production code with untested paths. Close these red/green before any new feature.
Full detail + what each test must assert: `notes/testing-gaps.md`. Order = security first.

1. **`storage.Encrypt` / `Decrypt`** — round-trip, wrong-key fails, tamper fails, empty input.
2. **`util.GenerateRandomString`** — length, charset, uniqueness, entropy-failure path.
3. **`GetObjectACLHandler`** — returns persisted ACL (private + public-read), NoSuchKey.
4. **`GetConfigHandler` / `SetPublicBaseURL`** — admin config read surface.
5. **`cmd` env parsing** — `parseBoolEnv`/`parseFloatEnv`/`parseIntEnv`/`loadEncryptionKey`/`bootstrapSuperUser` table tests.

Done when `go tool cover -func` shows no zero-coverage funcs except the genuinely
E2E-covered set listed in `notes/testing-gaps.md`.

## Phase 1 — S3 compatibility (close gaps tools actually hit)

Highest impact: these are the ops common clients call and currently fail on.

### 1. DeleteObjects (batch) — DONE (2026-06-06)
- Route: `POST /:bucket?delete`
- Parse XML `<Delete><Object><Key>...</Key></Object>...</Delete>` body
- Return `<DeleteResult>` with per-key `<Deleted>` / `<Error>` entries
- Honor `<Quiet>` mode (only return errors)
- Unblocks: `aws s3 rm --recursive`, rclone purge, mc rm
- Wire in `dispatchBucketSubresource` (POST is new on bucket path — add `g.POST("/:bucket", ...)`)
- Probe: hostile keys (traversal) rejected; valid batch delete works

### 2. CopyObject — DONE (2026-06-06)
- Route: `PUT /:bucket/*key` with `x-amz-copy-source` header present
- Same source==dest = metadata-only replace (`x-amz-metadata-directive: REPLACE`)
- Copy object bytes + `.meta` sidecar; recompute ETag
- Validate copy-source path through `ValidateObjectKey` (attacker-controlled)
- Enables rename/move flows
- Probe: copy-source traversal rejected; valid copy works

### 3. Conditional requests — DONE (2026-06-06)
- `If-Match` / `If-None-Match` / `If-Modified-Since` / `If-Unmodified-Since`
- GET: `304 Not Modified` / `412 Precondition Failed`
- PUT: `412` on mismatch (optimistic concurrency); If-None-Match:* = create-only
- NOTE: handled explicitly, NOT via http.ServeContent — gin defers WriteHeader so
  ServeContent's empty 304 was swallowed and surfaced as 200. See conditional.go.
- Phase 1 (S3 compatibility) COMPLETE.

## Phase 2 — durability & safety (harden what exists)

### 4. Atomic writes + fsync — DONE (2026-06-06)
- Object bytes written via temp file + rename in `streamToObject` (shared by
  upload + copy), so a crash mid-PUT never leaves a partial object.
- fsync of the temp file before rename + fsync of the parent dir after, gated on
  the durability setting. Configurable via SYNC_WRITES env (default on) and the
  admin Settings UI (persisted override wins, survives restart). See durability.go.
- REMAINING (minor): the `.meta` sidecar is still written in place, not fsync'd.
  Object data + dir entry are durable; the sidecar is reconstructable (ETag
  backfills on read), so this is acceptable. Revisit only if needed.

### 5. Concurrent-write safety — DONE (2026-06-06)
- Striped per-object lock (256 stripes, bounded memory) in locks.go, keyed by
  on-disk path. Shared by finalizeObjectWrite + removeObject so a PUT/PUT or
  PUT/DELETE on the same key can't interleave object rename vs sidecar write.
- TDD: a concurrency test asserting "meta ETag == md5(object bytes)" failed
  before the lock (torn pairs) and passes after.
- Phase 2 (durability & safety) COMPLETE.

## Phase A — admin panel (parallel track, ships independently of S3 phases)

Web UI features. Each = React page/component + any admin API + tests (unit for handler,
component test, and an E2E in `tests/` driving the real binary per the testing policy).
Existing pages: Login, Buckets, Objects, ObjectDetail, BucketCORS, Users, Settings.

### A1. Dashboard overview — DONE (2026-06-07)
- New landing page: bucket count, object count, total storage bytes, request metrics.
- Read-only, low risk. Data sources:
  - `/metrics` (Prometheus) is already exposed — parse counters for request stats, OR
  - add a small `GET /api/stats` admin handler that walks the store for
    bucket/object/byte totals (cache or compute on demand; bounded walk).
- UI: a few stat cards + a simple requests-over-time view from metrics. Quiet layout,
  clear focal numbers (per global UI rules — no card overuse, typography-led hierarchy).
- DECIDED (2026-06-06): use BOTH — `GET /api/stats` for storage totals
  (bucket/object/byte counts via bounded store walk) + `/metrics` for request-rate
  charts.

### A2. Object tagging editor (in ObjectDetail) — DONE (2026-06-07). getObjectTagging/putObjectTagging + editor UI.
### A3. Bucket ACL toggle — ALREADY DONE (pre-existing BucketsPage.onToggleACL).
### A4. Presigned link button — ALREADY DONE (pre-existing ObjectDetailPage.onPresign).
### A5. Audit log viewer — NEEDS BACKEND: ACL audit currently only slog'd, not stored.
   Requires persisting audit events before a read API/UI is possible. Larger; deferred.
### A6. Visual ACL editor for users — replace raw-JSON ACL with bucket x action matrix. UI polish.
### A7. Upload (drag-drop) + multi-select delete — DONE (2026-06-07). Upload pre-existed; added batch delete.
### A8. Copy / move / rename — DONE (2026-06-07). copyObject client + Rename (copy+delete) action.

## Phase 3 — feature depth (only with a real need)

- **Versioning** — version IDs, list versions, delete markers (large lift)
- **Bucket policy / lifecycle** — JSON policy eval, expiration rules
- **Server-side checksums** beyond ETag (SHA-256, CRC32C) per modern S3

## Conventions for every item (per CLAUDE.md)

- One handler + storage method + tests per change
- Add at least one `scripts/pentest/probes.sh` probe: negative (hostile → 4xx)
  AND positive regression (good input still works) before merge
- `go vet ./...` and `go test -count=1 ./...` green before commit
- Conventional Commits, lowercase subjects
- New subresource = wire both the dispatch table and the handler; nothing else

## Status log

- 2026-06-06: Roadmap created.
- 2026-06-06: Test audit — coverage not 100%, 5 real gaps (see notes/testing-gaps.md).
  Added Phase 0 (close gaps) as a hard blocker before Phase 1. Next up: Encrypt/Decrypt tests.
- 2026-06-06: Added Phase A (admin panel, parallel track). First: A1 Dashboard overview.
- 2026-06-06: Phase 0 DONE — all 5 gaps closed (crypto, rand, GetObjectACL, config,
  cmd env). util 0->87.5%, cmd 27.6->49.3%, storage 60.5->65.5%.
- 2026-06-06: Phase 1.1 DeleteObjects DONE — POST /:bucket?delete, XML+JSON, per-key
  validation (traversal/sidecar -> per-key Error), 1000-key cap. Shared removeObject
  helper (refactored out of DeleteObjectHandler). Unit tests + AWS-SDK E2E + pentest
  group (194 probes green).
- 2026-06-06: Phase 1.2 CopyObject DONE — PUT + x-amz-copy-source, COPY/REPLACE
  directive, validated copy-source (traversal/sidecar rejected), self-copy needs
  REPLACE. Refactored upload+copy onto a shared finalizeObjectWrite (temp+rename,
  which also delivers Phase 2.4 atomic writes partially). Caught+fixed a metadata
  casing bug via TDD. Unit + AWS-SDK E2E + pentest (204 probes green).
- 2026-06-06: Phase 1.3 conditional requests DONE — GET If-Match/If-None-Match/
  If-(Un)Modified-Since (304/412), PUT If-None-Match:*/If-Match (412). Explicit
  handling in conditional.go (gin's deferred WriteHeader swallows ServeContent's
  304). Fixed a stale Content-Length on short-circuit responses. Unit + E2E +
  pentest (210 probes green). PHASE 1 COMPLETE.
- 2026-06-06: Phase 2.4 atomic writes + fsync DONE — streamToObject (temp+rename),
  fsync data+dir gated on SYNC_WRITES (default on) + admin Settings toggle
  (persisted). New /api/config/sync endpoint + UI. Unit + E2E + pentest (215 green).
- 2026-06-06: Phase 2.5 concurrent-write safety DONE — striped per-object lock
  (locks.go) shared by write+delete. TDD torn-write test (red before, green after).
  PHASE 2 COMPLETE.
- 2026-06-07: Phase A1 dashboard DONE — GET /api/stats (bucket/object/byte totals
  via bounded store walk + cumulative request count from metrics registry) + React
  DashboardPage + nav. Unit + E2E + pentest (220 green).
- 2026-06-07: Phase A2 tagging editor DONE — s3.ts getObjectTagging/putObjectTagging
  + tag rows (add/remove/save) in ObjectDetailPage. Backend tagging already fully
  tested. Discovered A3 (bucket ACL toggle) + A4 (presign button) were already
  implemented.
- 2026-06-07: Phase A7 (batch delete) + A8 (rename/move) DONE — deleteObjects +
  copyObject web clients, selection UI + Rename action in ObjectsPage. Exercise the
  Phase 1 DeleteObjects/CopyObject backends (already E2E-tested) from the UI.
  REMAINING: A5 (audit viewer — needs audit-event persistence first, a backend
  feature), A6 (visual ACL matrix — polish over the working JSON editor),
  Phase 3 (versioning/lifecycle — large; roadmap-gated to "real need only").
- 2026-06-07: dashboard request-outcome work DONE — (1) scoped 2xx/4xx/5xx counter
  to real S3 traffic (storage :9000 + admin /api/s3 group), excluding admin
  polling/SPA/metrics; (2) per-minute time series: a background sampler snapshots
  the cumulative counters into BoltDB (RequestSamples), pruned to a configurable
  retention (default 30d, GET/PUT /api/config/retention); (3) navigable chart API
  GET /api/stats/requests?range=1h|24h|7d|14d|30d&offset=N (server rolls minute
  samples into <=60 wall-aligned buckets, nav bounded by retention); (4) CSS
  stacked-bar chart on the dashboard (no charting lib) with range picker +
  back/forward window nav + per-class totals, on-system monochrome (danger for
  5xx only). Visually verified via Playwright. Unit + E2E + pentest (232 green).
  NOTE: real time-series trending lives here now, not Grafana (user finds it heavy
  and rejected session-only client polling).
  NEXT (user-requested, not yet started): admin-panel E2E test coverage sweep.
