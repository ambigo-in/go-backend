package middleware

import (
	"net/http"
	"sync"
	"time"

	"ambigo-backend/api/response"
	"golang.org/x/time/rate"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipLimiter struct {
	mu          sync.Mutex
	clients     map[string]*clientLimiter
	rate        rate.Limit
	burst       int
	cleanupDone chan struct{}
}

func NewIPLimiter(r rate.Limit, burst int) *ipLimiter {
	l := &ipLimiter{
		clients:     make(map[string]*clientLimiter),
		rate:        r,
		burst:       burst,
		cleanupDone: make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

func (l *ipLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for ip, c := range l.clients {
			if time.Since(c.lastSeen) > 10*time.Minute {
				delete(l.clients, ip)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	c, exists := l.clients[ip]
	if !exists {
		c = &clientLimiter{
			limiter: rate.NewLimiter(l.rate, l.burst),
		}
		l.clients[ip] = c
	}
	c.lastSeen = time.Now()
	return c.limiter
}

func RateLimit(next http.HandlerFunc, limiter *ipLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if !limiter.getLimiter(ip).Allow() {
			response.Error(w, "Too many requests. Please try again later.", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}
