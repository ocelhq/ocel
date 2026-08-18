package apigateway

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	deleteAttempts = 30
	deleteEvery    = 30 * time.Second
	deleteJitter   = 0.05

	kindRestAPI = "REST API"
)

type Deleter struct {
	Wait     func(context.Context, time.Duration) error
	Attempts int
	Every    time.Duration
	Jitter   func() float64

	mu    sync.Mutex
	spent int
	begun bool
}

func NewDeleter() *Deleter {
	return &Deleter{Wait: waitFor, Attempts: deleteAttempts, Every: deleteEvery, Jitter: rand.Float64}
}

func (d *Deleter) attempts() int { return max(d.Attempts, 1) }

func (d *Deleter) every() time.Duration {
	if d.Every <= 0 {
		return deleteEvery
	}
	return d.Every
}

func (d *Deleter) jitter() float64 {
	if d.Jitter == nil {
		return rand.Float64()
	}
	return d.Jitter()
}

func (d *Deleter) interval() time.Duration {
	every := d.every()
	return every + time.Duration(float64(every)*deleteJitter*d.jitter())
}

func (d *Deleter) hold(ctx context.Context) error {
	if d.Wait != nil {
		return d.Wait(ctx, d.interval())
	}
	return waitFor(ctx, d.interval())
}

func (d *Deleter) drain(ctx context.Context, c Clients, ids []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	standing := slices.Clone(ids)
	var errs []error
	for at := 0; at < len(standing) && d.spent < d.attempts(); {
		if d.begun {
			if err := d.hold(ctx); err != nil {
				return errors.Join(append(errs, err, d.outstanding(standing))...)
			}
		}
		d.begun = true
		d.spent++
		err := deleteAPI(ctx, c, standing[at])
		switch {
		case err == nil:
			standing = slices.Delete(standing, at, at+1)
		case throttled(err):
		default:
			errs = append(errs, err)
			at++
		}
	}
	if len(standing) > 0 {
		errs = append(errs, d.outstanding(standing))
	}
	return errors.Join(errs...)
}

func (d *Deleter) outstanding(standing []string) error {
	return &edge.OutstandingError{
		Because: "API Gateway deletes at most one REST API every " + d.every().String() + " per account, and this run stopped with the queue unfinished",
		Waited:  time.Duration(max(d.spent-1, 0)) * d.every(),
		Items:   outstandingAPIs(standing),
	}
}

func outstandingAPIs(ids []string) []edge.Outstanding {
	items := make([]edge.Outstanding, 0, len(ids))
	for _, id := range ids {
		items = append(items, edge.Outstanding{Kind: kindRestAPI, Name: id})
	}
	return items
}

func waitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
