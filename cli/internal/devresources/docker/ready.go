package docker

import (
	"context"
	"fmt"
	"time"
)

const (
	firstProbeDelay = 50 * time.Millisecond
	maxProbeDelay   = time.Second
)

func WaitReady(ctx context.Context, within time.Duration, probe func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	delay := firstProbeDelay
	for {
		err := probe(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("not ready within %s: %w", within, err)
		case <-time.After(delay):
		}
		delay = min(delay*2, maxProbeDelay)
	}
}
