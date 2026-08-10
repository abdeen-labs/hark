# syntax=docker/dockerfile:1

# ── build ───────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build

WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

# Dependencies first so a source-only change reuses the cached layer. No
# BuildKit cache mounts: Railway's builder demands ids carrying its own
# per-service cacheKey prefix, and layer caching already covers the download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/harkd ./cmd/harkd

# ── runtime ─────────────────────────────────────────────────────────────────
FROM alpine:3.22

# ca-certificates: APNs is reached over TLS. tzdata: timestamps are rendered in
# UTC, but a correct zone database keeps time handling honest.
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 hark \
 && adduser  -S -u 10001 -G hark hark

COPY --from=build /out/harkd /usr/local/bin/harkd

USER hark:hark
EXPOSE 8080
ENV HARK_LISTEN_ADDR=:8080

# Uses the server's own readiness probe, which fails when PostgreSQL is
# unreachable.
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --quiet --spider --tries=1 http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/harkd"]
