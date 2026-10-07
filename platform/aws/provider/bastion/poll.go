package bastion

import (
	"context"
	"math/rand/v2"
	"time"
)

var pollInterval = 2 * time.Second

func pause(ctx context.Context, attempt int) error {
	delay := min(pollInterval<<min(attempt, 8), pollCeiling)
	delay += time.Duration(float64(delay) * pollJitter * (2*rand.Float64() - 1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
