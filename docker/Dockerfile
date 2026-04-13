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
COPY --from=builder /app/ByteBucket /ByteBucket
EXPOSE 9000 9001
VOLUME ["/data"]
ENTRYPOINT ["/ByteBucket"]
