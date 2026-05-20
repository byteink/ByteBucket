# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Common commands

```bash
make ui                                    # install web deps + build React bundle into internal/webui/dist
make build                                 # produce ./build/ByteBucket (after make ui)
make test                                  # CGO_ENABLED=0 go test -count=1 ./...
make vet                                   # go vet ./...
make pentest                               # black-box DAST: docker-compose with bytebucket + attacker container
make image-scan                            # build prod image + Trivy scan (vuln/misconfig/secret, HIGH+CRITICAL gate)

go run ./cmd/ByteBucket                    # local server (needs ENCRYPTION_KEY, ACCESS_KEY_ID, SECRET_ACCESS_KEY env vars)
go test -count=1 ./internal/handlers/      # unit tests for one package
CGO_ENABLED=0 go test -count=1 -run TestE2E_Multipart ./tests/...   # single E2E test (CGO off: see below)

cd web && npm run dev                      # Vite dev server (HMR) on :5173, proxies admin API to :9001

docker build -f docker/Dockerfile -t bytebucket:local .         # production image
docker compose -f docker/compose.dev.yml up                     # dev container with Air live-reload
```

The `internal/webui/dist/` directory is `//go:embed`-ed at compile time. The directory must exist for Go to build — `.keep` anchors it; `make ui` populates real assets. If you only have `.keep`, the binary builds but serves a placeholder UI.

E2E tests (`tests/`) need Docker running locally. They build `docker/Dockerfile` via testcontainers and drive the resulting container over HTTP. First run is slow (Docker build); subsequent runs hit the layer cache. Run them with `CGO_ENABLED=0` (as `make test` does): testcontainers pulls in `go-m1cpu`, whose cgo init segfaults under Go 1.26+ on darwin/arm64. We never call it, so the non-cgo stub is correct — and it matches the cgo-off binary we ship.

## Architecture

Two HTTP servers run in the same process, sharing one filesystem-backed store:

- **Port 9000** — SigV4 surface for S3 clients. XML wire format. Mounted in `internal/router/storage_router.go`.
- **Port 9001** — admin surface: `X-Admin-AccessKey`/`X-Admin-Secret` header auth, JSON wire format, plus the embedded React SPA at `/`. Mounted in `internal/router/admin_router.go`.

Both routers call `RegisterStorageRoutes` from `internal/router/storage_routes.go`, so **the same storage handlers serve both surfaces**. Handlers stay surface-agnostic: the auth middleware publishes the authenticated `*storage.User` on the Gin context, and handlers read it via `c.Get("user")`. Wire-format negotiation lives in `internal/handlers/respond.go` (`wantsJSON(c)` returns true for `/api` paths or explicit `Accept: application/json`; everything else gets S3 XML).

### Middleware chain (order matters)

Storage router: `RequestID → Log → Metrics → BucketCORS → ValidateNames → AuthMiddleware → handler`.
Admin router: `RequestID → Log → Metrics → AdminAuthMiddleware → ValidateNames → handler`.

`ValidateNames` (`internal/middleware/names.go`) is a security chokepoint — it runs before auth on the SigV4 surface specifically because the anonymous-read branch in `AuthMiddleware` constructs filesystem paths from `:bucket` and `*objectKey` during the public-read ACL lookup. Any URL-encoded path traversal must be rejected before that code runs.

### Storage layout

```
/data/
  users.db                            # BoltDB: users, ACLs, encrypted secrets
  objects/
    <bucket>/
      <object>                        # raw bytes
      <object>.meta                   # JSON sidecar: ETag, checksums, x-amz-meta-*, acl
      .cors.json                      # per-bucket CORS config
      .acl.json                       # per-bucket canned ACL (private | public-read)
  uploads/<bucket>/<uploadId>/        # multipart staging
```

Sidecars (`.meta`, `.cors.json`, `.acl.json`) are filtered from `ListObjects` at every recursion depth. The object-key validator (`storage.ValidateObjectKey`) rejects keys that would land on a sidecar file, so a hostile PutObject cannot clobber a bucket's ACL by uploading an "object" named `.acl.json`.

### User model

"Admin" is **not a flag** — it is an ACL pattern. A user is treated as admin iff their ACL contains `{"effect":"Allow","buckets":["*"],"actions":["*"]}`. Everything narrower is an S3-only user. The dashboard rejects login for non-admins. `internal/auth/auth.go::AdminAuthMiddleware` enforces this; `internal/auth/auth.go::AuthMiddleware` enforces per-action ACLs for SigV4 requests.

### ACLs (object visibility)

Two canned values only: `private` and `public-read`. Resolution precedence: object ACL wins, otherwise the bucket ACL, otherwise private. The anonymous-read branch in `AuthMiddleware` only permits `GET`/`HEAD` and never accepts subresources (`?acl`, `?uploads`, `?cors`) or writes — so a misconfigured ACL cannot escalate to anonymous writes. Presigned URLs use the existing SigV4 path and bypass the ACL gate.

### Critical files when changing surfaces

- New S3 subresource (`?something`) — wire it in both `dispatchBucketSubresource` and `dispatchObjectGET`/`PUT`/`DELETE` in `internal/router/storage_routes.go`.
- Persistence change — both the SigV4 handler and the admin handler share the storage layer; touch `internal/storage/*` and add tests under `internal/storage/*_test.go`.
- Sidecar filename change — update the filter list in `bucket.go::ListObjectsHandler` (`isSidecar`) and the reserved-name list in `storage/names.go::reservedBucketSidecars`.

## Conventions (project-specific)

- Conventional Commits, lowercase subjects (`feat(acl): ...`, `fix(list): ...`).
- Comments explain **why**, never what. If a future reader would ask "why is this here?", answer it; otherwise omit.
- No emojis anywhere — code, tests, docs, UI copy, commits.
- Handlers never re-derive identity; they read `c.Get("user")` (published by auth middleware).
- Errors flow through `respondError`; do not write error bodies directly.
- Test fixtures use BoltDB-style isolation: `t.TempDir()` plus `storage.ObjectsRoot = dir` + `t.Cleanup(...)`. There are two roots (`storage.ObjectsRoot` and the handler-package `objectsRoot`); when seeding bucket data in a handler test, set both.
- `go vet ./...` and `go test -count=1 ./...` must be green before every commit.
- **Every feature that adds or changes an attacker-reachable surface must add at least one probe to `scripts/pentest/probes.sh` before merge.** That includes new HTTP routes, new subresources (`?something`), new auth or ACL paths, and any change that touches name validation or sidecar handling. The probe asserts both the negative case (hostile input → 4xx) and a positive regression case (good input still works). `make pentest` must be green; the release preflight gates on it. See `scripts/pentest/README.md` for the patterns to imitate.

## Release flow

Tags drive releases. The `/release` slash command (`.claude/commands/release.md`) automates preflight (clean tree, up-to-date with origin/main, full test suite green) and pushes the tag. The `.github/workflows/release.yml` workflow builds multi-arch images on native runners (no QEMU), publishes to `ghcr.io/byteink/bytebucket`, and creates a GitHub Release. Pre-release tags (`vX.Y.Z-rc.N`) are excluded from the `latest` rotation automatically. Never force-push a tag; bump to the next version instead.

## Where to look first

- New AWS S3 op support → start at `internal/router/storage_routes.go` to see how subresources dispatch, then `internal/handlers/*.go` for the handler shape.
- Auth or signature issue → `internal/auth/auth.go` (header-auth, presigned-URL, anonymous-read paths are all here).
- Wire-format question (XML vs JSON) → `internal/handlers/respond.go`.
- Embedded UI not serving → `internal/webui/webui.go`; check that `internal/webui/dist/` has real assets (rerun `make ui`).
- Cross-surface parity / "does it work the same on 9000 and 9001?" → `tests/cross_surface_parity_test.go`.
