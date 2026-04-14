# =========================================================
# Production Build
# =========================================================

# Stage 1: build the React admin UI bundle.
FROM node:20-alpine AS ui
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2: build the Go binary with the bundled UI embedded via go:embed.
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the placeholder dist with the freshly built UI before compiling.
RUN rm -rf internal/webui/dist
COPY --from=ui /app/web/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -o ByteBucket ./cmd/ByteBucket

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

COPY --from=builder /app/ByteBucket /ByteBucket
EXPOSE 9000 9001
VOLUME ["/data"]
ENTRYPOINT ["/ByteBucket"]
