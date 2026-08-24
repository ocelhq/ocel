package values

import (
	"context"
	"sync"
)

const inFlight = 16

func each(ctx context.Context, n int, work func(context.Context, int) error) error {
	if n <= 1 {
		if n == 1 {
			return work(ctx, 0)
		}
		return nil
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	tokens := make(chan struct{}, inFlight)
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case tokens <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-tokens }()
			if err := work(ctx, i); err != nil {
				once.Do(func() {
					failure = err
					stop()
				})
			}
		}()
	}
	wg.Wait()
	return failure
}
