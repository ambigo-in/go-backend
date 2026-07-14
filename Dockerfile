FROM golang:1.26.4 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake git && rm -rf /var/lib/apt/lists/*

RUN git clone --depth 1 https://github.com/uber/h3.git /tmp/h3 \
    && mkdir -p /tmp/h3/build \
    && cd /tmp/h3/build \
    && cmake -DBUILD_SHARED_LIBS=ON -DCMAKE_INSTALL_PREFIX=/usr/local -DBUILD_TESTS=OFF .. \
    && make -j"$(nproc)" && make install && rm -rf /tmp/h3

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o server ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/lib/libh3.so* /usr/local/lib/
COPY --from=builder /app/server /server
RUN ldconfig
RUN groupadd -g 1000 appuser && useradd -m -u 1000 -g appuser appuser && chown appuser:appuser /server
USER appuser
WORKDIR /home/appuser

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/v1/health || exit 1
CMD ["/server"]
