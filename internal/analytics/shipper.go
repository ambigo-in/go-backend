package analytics

import (
	"context"
	"time"

	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
)

// Shipper is the postman: it stands at the bus, buffers events, and delivers
// them in batches to every configured Sink. Backpressure discipline matches
// the EventBus itself — when the buffer is full, events are dropped with a
// metric rather than blocking ride code.
type Shipper struct {
	bus        *eventbus.InMemoryBus
	sinks      []Sink
	buf        chan []byte
	stop       chan struct{}
	stopped    chan struct{}
	batchSize  int
	flushEvery time.Duration
}

const (
	defaultBatchSize  = 500
	defaultFlushEvery = 5 * time.Second
	maxAttempts       = 3
)

func NewShipper(bus *eventbus.InMemoryBus, sinks ...Sink) *Shipper {
	return &Shipper{
		bus:        bus,
		sinks:      sinks,
		buf:        make(chan []byte, 8192),
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
		batchSize:  defaultBatchSize,
		flushEvery: defaultFlushEvery,
	}
}

// Start subscribes to the canonical analytics channel list. Call once.
func (s *Shipper) Start() {
	for _, ch := range eventbus.AnalyticsChannels() {
		s.bus.Subscribe(ch, s.enqueue)
	}
	go s.run()
}

// Stop flushes buffered events and waits for the delivery loop to exit.
func (s *Shipper) Stop() {
	close(s.stop)
	<-s.stopped
}

func (s *Shipper) enqueue(payload []byte) {
	select {
	case s.buf <- payload:
	default:
		metrics.AnalyticsBufferDroppedTotal.Inc()
		logger.Log.Warn().Msg("Analytics shipper buffer full; dropping event")
	}
}

func (s *Shipper) run() {
	defer close(s.stopped)
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	batch := make([][]byte, 0, s.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.deliver(batch)
		batch = make([][]byte, 0, s.batchSize)
	}

	for {
		select {
		case ev := <-s.buf:
			batch = append(batch, ev)
			if len(batch) >= s.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.stop:
			for {
				select {
				case ev := <-s.buf:
					batch = append(batch, ev)
					if len(batch) >= s.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Shipper) deliver(batch [][]byte) {
	for _, sink := range s.sinks {
		for attempt := 1; ; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := sink.WriteBatch(ctx, batch)
			cancel()
			if err == nil {
				metrics.AnalyticsShippedTotal.WithLabelValues(sink.Name()).Add(float64(len(batch)))
				break
			}
			if attempt >= maxAttempts {
				metrics.AnalyticsShipErrorsTotal.WithLabelValues(sink.Name()).Add(float64(len(batch)))
				logger.Log.Error().Err(err).Str("sink", sink.Name()).Int("events", len(batch)).Msg("Analytics batch dropped after retries")
				break
			}
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
}
