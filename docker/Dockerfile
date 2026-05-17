# =========================================================
# Production Build
# =========================================================
#
# Hardening posture:
#  - scratch base: no shell, no libc, no busybox, no package manager,
#    no userland. Anything an attacker would shell out to is absent.
#  - non-root: UID/GID 65532 (Google distroless "nonroot" convention).
#    Numeric UID works on scratch without /etc/passwd because nothing
#    in the binary calls user.Current() — verified by grep.
#  - static binary: CGO disabled so the only file in the image (besides
#    /data) is the binary itself. No dynamic linker to attack.
#  - stripped: -trimpath + -ldflags "-s -w" remove build-host paths,
#    symbol table, and DWARF info. Cuts ~20% off binary size and
#    eliminates the easiest binary-forensics aids.
#  - exposed /data is pre-created with the non-root user as owner so
#    a fresh named volume inherits the right permissions on first run.

# Stage 1: build the React admin UI bundle.
FROM node:24-alpine AS ui
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2: build the Go binary with the bundled UI embedded via go:embed.
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the placeholder dist with the freshly built UI before compiling.
RUN rm -rf internal/webui/dist
COPY --from=ui /app/web/dist internal/webui/dist
# -trimpath: rewrite recorded source paths (no /root/, /home/runner/, etc.)
# -s -w:     drop the symbol table and DWARF debug info
# CGO=0:     pure-Go binary, no glibc / musl dependency
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ByteBucket ./cmd/ByteBucket

# Pre-stage /data with the runtime user as owner. scratch has no mkdir or
# chown, so we materialise the directory here and COPY it across. When the
# operator binds a named volume to /data Docker copies these contents in on
# first run, propagating the ownership we set below.
RUN mkdir -p /out/data \
 && chown -R 65532:65532 /out/data \
 && chmod 0755 /out/data

# Stage 3: minimal runtime.
FROM scratch

# OCI image metadata. `image.source` links the image to the repo so GHCR
# renders that repo's README on the package page. url/documentation are
# omitted because the repo is private — their links would 404 for
# anonymous viewers.
LABEL org.opencontainers.image.title="ByteBucket"
LABEL org.opencontainers.image.description="Self-hosted, S3-compatible object storage. One small Go binary with an embedded React admin UI."
LABEL org.opencontainers.image.source="https://github.com/byteink/ByteBucket"
LABEL org.opencontainers.image.licenses="SSPL-1.0"
LABEL org.opencontainers.image.vendor="byteink"

# The binary itself is fine as root-owned and world-readable+executable;
# the runtime user only needs execute. /data is owned by the non-root
# user so MkdirAll inside the binary succeeds on a fresh start.
COPY --from=builder /out/ByteBucket /ByteBucket
# --chown is redundant with the builder-stage chown but BuildKit does not
# always preserve numeric ownership across multi-stage COPY, so re-stamp
# the ownership explicitly. Without this the runtime user lacks write
# access to its own data root.
COPY --from=builder --chown=65532:65532 /out/data /data

# Drop privilege. Numeric UID:GID so this works without /etc/passwd. The
# operator can verify with `docker inspect --format '{{.Config.User}}'`.
USER 65532:65532

# Explicit SIGTERM matches the in-process graceful shutdown handler. The
# default would also be SIGTERM but pinning it removes the risk of a
# future base-image change altering it under us.
STOPSIGNAL SIGTERM

EXPOSE 9000 9001
VOLUME ["/data"]
ENTRYPOINT ["/ByteBucket"]
