package googleretry

import (
	"context"
	"time"
)

const (
	contentionBudget  = 5 * time.Minute
	contentionBackoff = time.Second
	contentionCeiling = 32 * time.Second
)

func UntilWritten(ctx context.Context, write func() error) error {
	return untilWritten(ctx, wallClock, write)
}

func UntilUncontended(ctx context.Context, contended func(error) bool, write func() error) error {
	return untilUncontended(ctx, wallClock, contended, write)
}

func untilWritten(ctx context.Context, c clock, write func() error) error {
	return untilUncontended(ctx, c, IsStale, write)
}

func untilUncontended(ctx context.Context, c clock, contended func(error) bool, write func() error) error {
	started := c.now()
	for conflicts := 0; ; conflicts++ {
		err := write()
		if !contended(err) {
			return err
		}
		wait := c.random(doubled(contentionBackoff, conflicts, contentionCeiling))
		if c.now().Sub(started)+wait > contentionBudget || (ctx.Err() == nil && c.isPastDeadline(ctx, wait)) {
			return err
		}
		if !c.sleep(ctx, wait) {
			return ctx.Err()
		}
	}
}

func doubled(base time.Duration, times int, limit time.Duration) time.Duration {
	for range times {
		if base >= limit {
			break
		}
		base *= 2
	}
	return min(base, limit)
}
