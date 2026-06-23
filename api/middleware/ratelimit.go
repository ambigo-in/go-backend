package middleware

import (
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

type ipLimiter struct {
	mu      sync.Mutex
	clients map[string]*rate.Limiter
	rate    rate.Limit
	burst   int
}

func NewIPLimiter(r rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{
		clients: make(map[string]*rate.Limiter),
		rate:    r,
		burst:   burst,
	}
}

func (l *ipLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	limiter, exists := l.clients[ip]
	if !exists {
		limiter = rate.NewLimiter(l.rate, l.burst)
		l.clients[ip] = limiter
	}
	return limiter
}

func RateLimit(next http.HandlerFunc, limiter *ipLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if !limiter.getLimiter(ip).Allow() {
			http.Error(w, "Too many requests. Please try again later.", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}
