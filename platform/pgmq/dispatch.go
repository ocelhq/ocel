package pgmq

import (
	"context"
	"sync"
)

func (e *Engine) Dispatch(ctx context.Context) error {
	var wg sync.WaitGroup
	loops := map[string]context.CancelFunc{}
	wg.Go(func() { e.sweep(ctx) })
	wg.Go(func() { e.fireSchedules(ctx) })
	receiver := queue{engine: e}
	for {
		deployed := e.current().queues()
		for name, stop := range loops {
			if _, still := deployed[name]; !still {
				stop()
				delete(loops, name)
			}
		}
		for name, of := range deployed {
			if _, running := loops[name]; running {
				continue
			}
			queueCtx, stop := context.WithCancel(ctx)
			loops[name] = stop
			wg.Go(func() { e.runs.Serve(queueCtx, receiver, of.topic, of.consumer) })
		}
		select {
		case <-ctx.Done():
			for _, stop := range loops {
				stop()
			}
			wg.Wait()
			return ctx.Err()
		case <-e.applied:
		}
	}
}
