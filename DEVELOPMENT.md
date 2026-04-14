# Development

Contributor / hacker notes. Users of the published image don't need any of this — see [README.md](README.md).

## Prerequisites

- Go 1.24 or later
- Node 20 or later (for the React admin UI)
- Docker and Docker Compose (for E2E tests and packaging)
- GNU `make` (the provided Makefile is portable but uses `find ... -delete`)

## Layout

```
cmd/ByteBucket/        # main entry point (server lifecycle, shutdown, slog setup)
internal/auth/         # SigV4 + admin auth middleware; user in context
internal/handlers/     # bucket, object, multipart, per-bucket CORS handlers
internal/middleware/   # request ID, slog logger, Prometheus metrics, body limits
internal/router/       # Gin routers for storage (9000) and admin (9001)
internal/storage/      # BoltDB user store, object filesystem, multipart, bucket CORS
internal/webui/        # embed.FS + SPA handler (serves React bundle)
internal/util/         # small shared helpers
tests/                 # E2E via testcontainers; builds docker/Dockerfile and drives real HTTP
web/                   # React + Vite + Tailwind admin UI
docker/                # Dockerfile, Dockerfile.dev, compose files
```

## First-time setup

```bash
git clone git@github.com:byteink/ByteBucket.git
cd ByteBucket
make ui                    # installs web deps, builds the React bundle, copies into internal/webui/dist
go mod download
```

## Running locally

```bash
export ENCRYPTION_KEY="$(openssl rand -base64 32)"
export ACCESS_KEY_ID="admin"
export SECRET_ACCESS_KEY="changeme"
export LOG_FORMAT=text     # human-readable logs during dev
go run ./cmd/ByteBucket
```

- S3: http://localhost:9000
- Admin API + UI: http://localhost:9001
- Metrics: http://localhost:9001/metrics

### Vite dev loop for UI work

Two terminals:

```bash
# terminal 1 — the server
go run ./cmd/ByteBucket

# terminal 2 — Vite with HMR against the running server
cd web && npm run dev
# UI at http://localhost:5173, admin API calls proxied to :9001
```

### Docker dev mode

```bash
docker compose -f docker/compose.dev.yml up
```

Uses `docker/Dockerfile.dev` with Air for live reload; mounts the source tree.

## Tests

```bash
go test -count=1 ./...
```

- Unit tests live next to code (`*_test.go`).
- E2E tests in `tests/` build the Dockerfile via testcontainers and drive the resulting container over real HTTP. First run is slow (Docker build); subsequent runs use the layer cache and finish in 20-30 seconds.

Useful subsets:

```bash
go test -count=1 ./internal/auth/...
go test -count=1 -run TestCrossSurfaceParity ./tests/...
go test -count=1 -run TestE2E_Multipart ./tests/...
go vet ./...
```

### Test writing conventions

- Unit tests use `httptest` + a local Gin engine per test so middleware stacks are explicit.
- E2E tests share one testcontainer via `TestMain`; subtests run against the same container to keep Docker rebuild cost amortised.
- For any bug fix: write the failing test first, see it fail for the right reason, then fix. The `fix(auth)` commits from 2026-04 follow this pattern and are a good reference.

## Building

### Local binary

```bash
make ui               # rebuild the UI bundle first
make build            # produces ./build/ByteBucket
```

### Production image

```bash
docker build -f docker/Dockerfile -t bytebucket:local .
```

The Dockerfile is multi-stage: node builds the UI, golang builds the binary embedding the UI, the final stage is `scratch` + the binary. No shell, no package manager, no stray files in the final image.

## Releasing

Tags drive releases. `/release` (Claude project command at `.claude/commands/release.md`) automates the preflight. The manual path:

```bash
git tag -a vX.Y.Z -m "release: vX.Y.Z"
git push origin vX.Y.Z
```

The `release.yml` workflow then:

1. Builds `linux/amd64` on `ubuntu-latest` and `linux/arm64` on `ubuntu-24.04-arm` (native, no QEMU).
2. Publishes per-platform digests to GHCR.
3. Merges them into a single manifest list with the full semver tag family (`vX.Y.Z`, `X.Y.Z`, `X.Y`, `X`, and `latest` if not a pre-release).
4. Creates a GitHub Release with auto-generated notes.

Pre-releases (`vX.Y.Z-rc.N`) are recognised by the hyphen and excluded from the `latest` / major / minor rotation.

## Conventions

- Commit messages follow Conventional Commits (`feat(scope): subject` etc.). Subject lines stay lowercase.
- Comments explain **why**, not what. If a future reader would ask "why is this here?", answer it; otherwise skip.
- No emojis in code, tests, docs, UI copy, or commits.
- Handlers stay surface-agnostic — auth middleware publishes the user onto the Gin context; handlers read `c.MustGet("user")` and don't re-derive identity.
- Content negotiation lives in `internal/handlers/respond.go` — XML for SigV4 routes, JSON for admin routes.
- Errors go through `respondError`; never write an error body directly.
- `go vet ./...` and the full test suite must pass before every commit.

## Debugging tips

- Every response carries `x-amz-request-id`. Grep the log for the same UUID to get the full story of one request.
- `LOG_FORMAT=text LOG_LEVEL=debug` for a human-readable dev stream.
- Prometheus metrics are exposed from the first moment the server starts; `curl -s localhost:9001/metrics | grep http_requests_total` after a few requests is a quick sanity check.
- BoltDB lives at `/data/users.db`. The `bbolt` CLI (`go install go.etcd.io/bbolt/cmd/bbolt@latest`) can dump buckets: `bbolt buckets /data/users.db`.
- Object sidecars are plain JSON: `cat /data/objects/<bucket>/<object>.meta | jq` tells you what the server thinks about an object.

## Deferred items

See [SECURITY.md](SECURITY.md) for the security backlog (sessions, CSRF, login rate limiting, TOTP, in-process TLS). Functional gaps (versioning, object locking, SSE-S3, lifecycle, replication) are not tracked in a file — open an issue if you hit one.
