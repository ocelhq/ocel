package envvars

import (
	"context"

	"golang.org/x/sync/errgroup"
)

const maxConcurrency = 16

func forEachConcurrently(ctx context.Context, n int, work func(context.Context, int) error) error {
	if n <= 1 {
		if n == 1 {
			return work(ctx, 0)
		}
		return nil
	}

	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrency)
	for i := range n {
		group.Go(func() error { return work(ctx, i) })
	}
	return group.Wait()
}
