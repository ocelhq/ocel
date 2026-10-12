package infra

import (
	"context"
	"fmt"
	"sync/atomic"

	"ocel.dev"
)

type Greeting struct {
	Name string `json:"name"`
}

type Greeted struct {
	Greeting  string `json:"greeting"`
	Starts    int64  `json:"starts"`
	WrappedBy string `json:"wrappedBy"`
	Audited   string `json:"audited"`
}

var (
	starts    atomic.Int64
	wrappedBy atomic.Value
	audited   atomic.Value
)

var Background = ocel.Worker("worker",
	ocel.OnStart(func(context.Context) error {
		starts.Add(1)
		return nil
	}),
	ocel.Middleware(func(ctx context.Context, next func(context.Context) error) error {
		if run, ok := ocel.RunFrom(ctx); ok {
			wrappedBy.Store(fmt.Sprintf("%s:%s", run.Kind, run.Name))
		}
		return next(ctx)
	}),
)

var Greet = ocel.Task("greet", func(_ context.Context, payload Greeting) (Greeted, error) {
	wrapped, _ := wrappedBy.Load().(string)
	consumed, _ := audited.Load().(string)
	return Greeted{Greeting: "hello " + payload.Name, Starts: starts.Load(), WrappedBy: wrapped, Audited: consumed}, nil
}, ocel.UseWorker(Background))

var Orders = ocel.Topic[Greeting]("orders")

var Audit = Orders.Consumer("audit", func(_ context.Context, payload Greeting) error {
	wrapped, _ := wrappedBy.Load().(string)
	audited.Store(wrapped + " " + payload.Name)
	return nil
}, ocel.UseWorker(Background))
