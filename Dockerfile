# ============================================================
# Ambigo Go Backend — Production Dockerfile for Google Cloud Run
# ============================================================
#
# Multi-stage build:
#   Stage 1 (builder): compiles Go binary with CGO (needed for h3-go)
#   Stage 2 (runtime): minimal Alpine image
#
# Cloud Run specifics:
#   - Listens on $PORT (injected by Cloud Run, defaults to 8080)
#   - Runs as non-root (UID 65534)
#   - No .env file — all config via Cloud Run env vars / Secret Manager
#   - Firebase credentials via Secret Manager mounted file
#   - Migrations are embedded in the binary via //go:embed *.sql
#
# NOTE: h3-go v4 uses CGO to link Uber's H3 C library, so we
#       cannot use CGO_ENABLED=0 or distroless. Alpine with musl
#       is the smallest viable alternative.
# ============================================================

# ---- Stage 1: Build ----
FROM golang:1.26-alpine AS builder

# Install C toolchain (required for h3-go CGO bindings) + certificates
RUN apk --no-cache add ca-certificates git gcc musl-dev

WORKDIR /build

# Cache dependency downloads (these layers rarely change)
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source (respects .dockerignore)
COPY . .

# Build with CGO enabled (required for h3-go C bindings)
# Static linking via -extldflags=-static so the binary runs on minimal Alpine
# -ldflags: strip debug symbols (-s) and DWARF tables (-w) → smaller binary
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w -linkmode=external -extldflags=-static" \
    -o /ambigo-server ./cmd/server

# ---- Stage 2: Runtime ----
# Alpine is required because h3-go uses CGO. Even with static linking,
# alpine is the safest minimal base that includes ca-certificates and
# timezone data without shell bloat.
FROM alpine:3.21

# Labels for container registry
LABEL maintainer="Ambigo Team"
LABEL org.opencontainers.image.source="https://github.com/Kaipapurandeswarreddy/ambigo-backend"

# Runtime dependencies only (no gcc, no dev headers)
RUN apk --no-cache add ca-certificates tzdata \
    && adduser -D -u 10001 appuser

WORKDIR /app

# Copy only the compiled binary from builder
COPY --from=builder /ambigo-server /app/ambigo-server

# Cloud Run injects PORT env var (default 8080)
ENV PORT=8080
EXPOSE 8080

# Run as non-root
USER 10001

ENTRYPOINT ["/app/ambigo-server"]
