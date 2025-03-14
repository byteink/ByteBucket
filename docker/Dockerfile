# =========================================================
# Production Build
# =========================================================

FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
RUN go mod tidy
COPY . .
RUN CGO_ENABLED=0 go build -o ByteBucket ./cmd/ByteBucket

FROM scratch
COPY --from=builder /app/ByteBucket /ByteBucket
EXPOSE 9000
VOLUME ["/data"]
ENTRYPOINT ["/ByteBucket"]