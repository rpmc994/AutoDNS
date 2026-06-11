# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Required for CGO (bbolt is pure Go, but we want a fully static binary).
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

# Cache dependency downloads separately from source compilation.
# go.sum is generated here if not already present (GOFLAGS=-mod=mod allows
# the toolchain to create/update go.sum during the build).
COPY go.mod ./
RUN GOFLAGS=-mod=mod go mod download

# Copy source.
COPY . .

# Build a fully static, stripped binary.
# Use the TARGETARCH build arg set by `docker buildx` so the image works on
# both amd64 (x86 servers) and arm64 (Raspberry Pi, Apple Silicon, etc.).
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build \
      -mod=mod \
      -ldflags="-s -w" \
      -trimpath \
      -o /autodns \
      .

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# scratch gives the absolute minimal attack surface — just the binary and
# the timezone database we copied from the builder.
FROM scratch

# Copy timezone data so Europe/London resolution works at runtime.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy trusted CA certificates for HTTPS calls (ipify, Cloudflare).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the compiled binary.
COPY --from=builder /autodns /autodns

# Create a non-root UID/GID mapping.  scratch has no passwd/group files,
# so we use numeric IDs directly.  UID 10001 is a common unprivileged choice.
#
# IMPORTANT: the host directory mounted at /data must be owned by UID 10001
# before the container starts, e.g.:
#   mkdir -p /host/data && chown 10001:10001 /host/data
USER 10001:10001

# Data directory — mount a host volume here in production.
VOLUME ["/data"]

ENV DATA_DIR=/data \
    LISTEN_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/autodns"]
