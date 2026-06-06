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

### 4. Atomic writes + fsync — PARTIAL (2026-06-06)
- DONE: object bytes now written via temp file + rename in `finalizeObjectWrite`
  (shared by upload + copy), so a crash mid-PUT no longer leaves a partial object.
- TODO: `fsync` the temp file before rename, and fsync the parent dir after, for
  true crash durability. The `.meta` sidecar is still written in place (not yet
  temp+renamed).

### 5. Concurrent-write safety
- Per-object lock so two PUTs to the same key can't interleave object vs sidecar
- Prevents torn object/metadata pairs under concurrent load

## Phase A — admin panel (parallel track, ships independently of S3 phases)

Web UI features. Each = React page/component + any admin API + tests (unit for handler,
component test, and an E2E in `tests/` driving the real binary per the testing policy).
Existing pages: Login, Buckets, Objects, ObjectDetail, BucketCORS, Users, Settings.

### A1. Dashboard overview — START HERE
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

### A2. Object tagging editor (in ObjectDetail) — `?tagging` API already exists.
### A3. Bucket ACL toggle (private <-> public-read) — `?acl` already exists.
### A4. Presigned link button (in ObjectDetail) — `?presign` already exists.
### A5. Audit log viewer — ACL-change audit already recorded (`AuditACLChange`); add read-only trail.
### A6. Visual ACL editor for users — replace raw-JSON ACL with bucket x action matrix.
### A7. Upload (drag-drop) + multi-select delete — delete pairs with Phase 1 DeleteObjects.
### A8. Copy / move / rename — pairs with Phase 1 CopyObject.

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
  pentest (210 probes green). PHASE 1 COMPLETE. Next: Phase 2 durability (fsync)
  or Phase A admin dashboard.
