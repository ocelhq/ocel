package docker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	firstProbeDelay = 50 * time.Millisecond
	maxProbeDelay   = time.Second
)

func WaitReady(ctx context.Context, engine Engine, id string, within time.Duration, probe func(context.Context) error) error {
	return poll(ctx, within, func(ctx context.Context) error {
		err := probe(ctx)
		if err == nil {
			return nil
		}
		if exited, _ := engine.ReadExit(ctx, id); exited != nil {
			return exited
		}
		return err
	})
}

func poll(ctx context.Context, within time.Duration, probe func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	delay := firstProbeDelay
	for {
		err := probe(ctx)
		if err == nil {
			return nil
		}
		if exited := (*Exited)(nil); errors.As(err, &exited) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("not ready within %s: %w", within, err)
		case <-time.After(delay):
		}
		delay = min(delay*2, maxProbeDelay)
	}
}
