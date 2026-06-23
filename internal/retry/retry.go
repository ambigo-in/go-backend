package retry

import (
	"context"
	"math"
	"math/rand"
	"time"
)

type Config struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

var Default = Config{
	MaxAttempts: 3,
	BaseDelay:   100 * time.Millisecond,
	MaxDelay:    2 * time.Second,
}

func Do(ctx context.Context, cfg Config, fn func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt == cfg.MaxAttempts-1 {
			break
		}
		delay := time.Duration(math.Min(
			float64(cfg.BaseDelay)*math.Pow(2, float64(attempt)),
			float64(cfg.MaxDelay),
		))
		jitter := time.Duration(rand.Int63n(int64(delay / 2)))
		timer := time.NewTimer(delay + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
