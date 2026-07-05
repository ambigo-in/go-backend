package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RideRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ambigo_ride_requests_total",
		Help: "Total number of ride requests",
	})

	RidesAssignedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ambigo_rides_assigned_total",
		Help: "Total number of rides successfully assigned",
	})

	RidesCompletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ambigo_rides_completed_total",
		Help: "Total number of rides completed",
	})

	RidesCancelledTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ambigo_rides_cancelled_total",
		Help: "Total number of rides cancelled",
	})

	ActiveConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ambigo_ws_active_connections",
		Help: "Current number of active WebSocket connections",
	})

	GoogleAPIDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ambigo_google_api_duration_seconds",
		Help:    "Latency of Google Routes API calls",
		Buckets: []float64{0.1, 0.5, 1, 2, 3, 5, 10},
	})

	DispatchLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ambigo_dispatch_latency_seconds",
		Help:    "Time from ride creation to driver assignment",
		Buckets: []float64{1, 3, 5, 10, 20, 30, 60},
	})

	HttpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ambigo_http_request_duration_seconds",
		Help:    "HTTP request latency by path",
		Buckets: []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 5},
	}, []string{"path", "method", "status"})

	HttpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ambigo_http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"path", "method", "status"})

	OtpRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ambigo_otp_requests_total",
		Help: "Total number of OTP requests",
	})

	EventBusMessagesDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ambigo_eventbus_messages_dropped_total",
		Help: "Number of EventBus messages dropped because subscriber channels were full",
	}, []string{"channel"})
)

func ObserveHttpRequest(path, method string, status int, dur time.Duration) {
	statusStr := strconv.Itoa(status)
	HttpRequestDuration.WithLabelValues(path, method, statusStr).Observe(dur.Seconds())
	HttpRequestsTotal.WithLabelValues(path, method, statusStr).Inc()
}

func ObserveGoogleAPI(dur time.Duration) {
	GoogleAPIDuration.Observe(dur.Seconds())
}

func ObserveDispatchLatency(dur time.Duration) {
	DispatchLatency.Observe(dur.Seconds())
}
