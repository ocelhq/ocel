package gcloud

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	WaitAttempts = 60
	WaitCeiling  = 5 * time.Second
)

func Until[T any](ctx context.Context, doing string, ask func() (T, error), done func(T) bool) (T, error) {
	return Waiting(ctx, Patience{Attempts: WaitAttempts, Ceiling: WaitCeiling}, doing, ask, done)
}

type Patience struct {
	Attempts int
	Ceiling  time.Duration
}

func Waiting[T any](
	ctx context.Context,
	limits Patience,
	doing string,
	ask func() (T, error),
	done func(T) bool,
) (T, error) {
	var nothing T
	for attempt := range limits.Attempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, limits.Ceiling) {
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
		doing, limits.Attempts)
}
