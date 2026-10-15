package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"

	"ambigo-backend/internal/metrics"
)

// uuidSeg matches UUID path segments (ride IDs, driver IDs). Without
// normalization every ride mints brand-new metric series
// (/rides/<uuid>/accept), which explodes Prometheus cardinality — fatal on
// the free tier (10k series) at real ride volumes.
var uuidSeg = regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func normalizePath(p string) string {
	return uuidSeg.ReplaceAllString(p, "/{id}")
}

func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		dur := time.Since(start)
		metrics.ObserveHttpRequest(normalizePath(r.URL.Path), r.Method, wrapped.status, dur)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack implements the http.Hijacker interface, allowing WebSockets to upgrade through this middleware.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("the ResponseWriter doesn't support the Hijacker interface")
	}
	return hijacker.Hijack()
}
