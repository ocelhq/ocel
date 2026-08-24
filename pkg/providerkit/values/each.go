package values

import (
	"context"

	"golang.org/x/sync/errgroup"
)

const inFlight = 16

func each(ctx context.Context, n int, work func(context.Context, int) error) error {
	if n <= 1 {
		if n == 1 {
			return work(ctx, 0)
		}
		return nil
	}

	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(inFlight)
	for i := range n {
		group.Go(func() error { return work(ctx, i) })
	}
	return group.Wait()
}
