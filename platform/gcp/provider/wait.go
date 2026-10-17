package gcp

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	waitAttempts = 60
	waitCeiling  = 5 * time.Second
)

func until[T any](ctx context.Context, doing string, ask func() (T, error), done func(T) bool) (T, error) {
	return waiting(ctx, patience{attempts: waitAttempts, ceiling: waitCeiling}, doing, ask, done)
}

type patience struct {
	attempts int
	ceiling  time.Duration
}

func waiting[T any](
	ctx context.Context,
	limits patience,
	doing string,
	ask func() (T, error),
	done func(T) bool,
) (T, error) {
	var nothing T
	for attempt := range limits.attempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, limits.ceiling) {
			return nothing, fmt.Errorf("wait for %s: %w", doing, ctx.Err())
		}
		value, err := ask()
		if err != nil {
			return nothing, err
		}
		if done(value) {
			return value, nil
		}
	}
	return nothing, refusal.Refuse(refusal.CodeNotReady,
		"%s is still not done after %d attempts, and going on before it is leaves the work half made.\n"+
			"It may still be provisioning: run the same command again once Google has caught up",
		doing, limits.attempts)
}
