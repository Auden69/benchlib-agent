# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY . .

RUN GOPROXY=https://proxy.golang.org,direct \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /benchlib-agent \
    ./cmd/agent

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /benchlib-agent /app/benchlib-agent

# Répertoire de données persistant (config.yaml)
VOLUME ["/data"]

EXPOSE 8090

ENV TZ=Europe/Paris

ENTRYPOINT ["/app/benchlib-agent", "--config", "/data/config.yaml"]