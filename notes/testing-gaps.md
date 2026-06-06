# Testing gaps (audit 2026-06-06)

STATUS: all five gaps CLOSED 2026-06-06 (Phase 0 done). Kept as a record + the
baseline numbers. The suite is ~190 test funcs (35 E2E); request paths are well
covered. E2E drives a containerized binary, so `go cover` understates real
coverage — judge by behavior, not the %.

## Measured unit coverage (baseline)

| Package | Coverage |
|---|---|
| middleware | 81.2% |
| auth | 65.6% |
| handlers | 63.0% |
| storage | 60.5% |
| webui | 56.5% |
| router | 41.6% |
| cmd | 27.6% |
| util | 0.0% |

## Genuine gaps — no unit AND no E2E coverage

Priority order (security first) — all DONE:

1. [DONE] **`storage.Encrypt` / `Decrypt`** (`internal/storage/storage.go:81,99`) — secret
   encryption at rest. Need: round-trip test, wrong-key fails, tamper/ciphertext
   corruption fails, empty input.
2. [DONE] **`util.GenerateRandomString`** (`internal/util/rand.go:14`) — used for secret/key
   generation. Need: length correctness, charset, uniqueness across N calls, error
   path when entropy source fails.
3. [DONE] **`GetObjectACLHandler`** (`internal/handlers/acl.go:252`) — Put is tested, Get is
   not asserted. Need: returns persisted ACL (private + public-read), NoSuchKey path.
4. [DONE] **`GetConfigHandler` / `SetPublicBaseURL`** (`internal/handlers/config.go`) — admin
   config read surface, untested.
5. [DONE] **`cmd` env parsing** (`parseBoolEnv` / `parseFloatEnv` / `parseIntEnv` /
   `loadEncryptionKey` / `bootstrapSuperUser`, `cmd/ByteBucket/main.go`) — table tests
   for valid/invalid/missing env; loadEncryptionKey rejects bad-length keys.

## Functions reading 0% in `go cover` but covered by E2E (NOT gaps)

DeleteObjectHandler, admin user CRUD, dispatchObject*, HeadBucketHandler,
PresignObjectHandler, ValidateNames — verified exercised by `tests/` E2E and/or
pentest probes. Listed here so a future audit doesn't re-flag them.

## How to verify progress

```bash
CGO_ENABLED=0 go test -count=1 -coverprofile=/tmp/cov.out ./internal/... ./cmd/...
go tool cover -func=/tmp/cov.out | awk '$3=="0.0%"'   # remaining zero-coverage funcs
```
