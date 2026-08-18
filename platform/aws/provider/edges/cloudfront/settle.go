package cloudfront

import (
	"context"
	"math/rand/v2"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	settleAttempts = 60
	settleEvery    = 15 * time.Second
	settleJitter   = 0.05

	deployedStatus = "Deployed"
)

type Settler struct {
	Wait     func(context.Context, time.Duration) error
	Attempts int
	Every    time.Duration
	Jitter   func() float64
}

func NewSettler() Settler {
	return Settler{Wait: waitFor, Attempts: settleAttempts, Every: settleEvery, Jitter: rand.Float64}
}

func (s Settler) attempts() int { return max(s.Attempts, 1) }

func (s Settler) every() time.Duration {
	if s.Every <= 0 {
		return settleEvery
	}
	return s.Every
}

func (s Settler) jitter() float64 {
	if s.Jitter == nil {
		return rand.Float64()
	}
	return s.Jitter()
}

func (s Settler) interval() time.Duration {
	every := s.every()
	spread := settleJitter * (2*s.jitter() - 1)
	return every + time.Duration(float64(every)*spread)
}

func (s Settler) window() time.Duration {
	return time.Duration(s.attempts()-1) * s.every()
}

func (s Settler) hold(ctx context.Context) error {
	if s.Wait != nil {
		return s.Wait(ctx, s.interval())
	}
	return waitFor(ctx, s.interval())
}

func (s Settler) settled(ctx context.Context, kind, id string, status func(context.Context) (string, error)) error {
	for attempt := 0; attempt < s.attempts(); attempt++ {
		if attempt > 0 {
			if err := s.hold(ctx); err != nil {
				return err
			}
		}
		state, err := status(ctx)
		if err != nil {
			if !throttled(err) {
				return err
			}
			continue
		}
		if state == deployedStatus {
			return nil
		}
	}
	return &edge.OutstandingError{
		Because: "CloudFront was still rolling the disable out to its edges, so nothing may be deleted yet",
		Waited:  s.window(),
		Items:   []edge.Outstanding{{Kind: kind, Name: id}},
	}
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
