# Multi-stage build for optimized image size
# Stage 1: Builder
FROM golang:1.26.4 AS builder

# Install build dependencies (gcc, make, cmake) and git
RUN apt-get update && \
    apt-get install -y --no-install-recommends build-essential ca-certificates git cmake pkg-config && \
    rm -rf /var/lib/apt/lists/*

# Build and install the H3 C library (native dependency for github.com/uber/h3-go)
RUN git clone --depth 1 https://github.com/uber/h3.git /tmp/h3 && \
    mkdir -p /tmp/h3/build && \
    cd /tmp/h3/build && \
    cmake -DBUILD_SHARED_LIBS=ON -DCMAKE_INSTALL_PREFIX=/usr/local -DBUILD_TESTS=OFF .. && \
    make -j"$(nproc)" && \
    make install && \
    rm -rf /tmp/h3

# Set working directory
WORKDIR /app

# Copy go mod files and download modules early for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application with CGO enabled so h3 C bindings link correctly
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o server ./cmd/server

# Stage 2: Runtime
FROM debian:trixie-slim

# Install runtime dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata wget && \
    rm -rf /var/lib/apt/lists/*

# Copy the H3 native library from builder and the built binary
COPY --from=builder /usr/local/lib/libh3.so* /usr/local/lib/
COPY --from=builder /app/server /home/appuser/server

# Ensure dynamic linker cache is updated so libh3 is found
RUN ldconfig || true

# Create non-root user for security
RUN groupadd -g 1000 appuser && \
    useradd -m -u 1000 -g appuser appuser && \
    chown appuser:appuser /home/appuser/server

# Set working directory
WORKDIR /home/appuser

# Switch to non-root user
USER appuser

# Expose port (Cloud Run uses the PORT env variable, default 8080)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/v1/health || exit 1

# Run the application
CMD ["./server"]
