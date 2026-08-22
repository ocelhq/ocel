package providerkit

import (
	"context"
	"errors"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type answering struct {
	kind  edge.Kind
	after int
	asked int
}

func (a *answering) Serving(context.Context, string) (edge.Kind, error) {
	a.asked++
	if a.asked > a.after {
		return a.kind, nil
	}
	return "", nil
}

func waiting(resolve Resolver, attempts int) (settler, *int) {
	slept := 0
	return settler{
		kind:     "relay",
		resolve:  resolve,
		attempts: attempts,
		wait:     time.Second,
		now:      func() time.Time { return time.Unix(1700000000, 0) },
		sleep: func(context.Context, time.Duration) error {
			slept++
			return nil
		},
	}, &slept
}

func TestSettleWaitsUntilTheHostnameAnswers(t *testing.T) {
	t.Parallel()
	resolve := &answering{kind: "relay", after: 2}
	settle, slept := waiting(resolve, 5)

	probe, err := settle.await(context.Background(), "app.acme.com", func(string) {})
	if err != nil {
		t.Fatalf("await() = %v, want it to wait the hostname out", err)
	}
	if !probe.OK || probe.Edge != "relay" {
		t.Errorf("await() = %+v, want a probe answered by the relay edge", probe)
	}
	if *slept != 2 {
		t.Errorf("await() slept %d time(s), want the two it waited through", *slept)
	}
}

func TestSettleGivesUpAfterItsLastAttempt(t *testing.T) {
	t.Parallel()
	resolve := &answering{kind: "relay", after: 99}
	settle, slept := waiting(resolve, 3)

	probe, err := settle.await(context.Background(), "app.acme.com", func(string) {})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal naming the wait", err)
	}
	if probe.OK {
		t.Error("await() recorded a passing probe for a hostname that never answered")
	}
	if resolve.asked != 3 {
		t.Errorf("await() asked %d time(s), want the 3 attempts it was given", resolve.asked)
	}
	if *slept != 2 {
		t.Errorf("await() slept %d time(s), want one fewer than its attempts", *slept)
	}
}

func TestSettleStopsWhenAnotherEdgeAnswers(t *testing.T) {
	t.Parallel()
	settle, _ := waiting(&answering{kind: "direct"}, 2)

	probe, err := settle.await(context.Background(), "app.acme.com", func(string) {})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if probe.Edge != "direct" {
		t.Errorf("await() recorded %q, want the edge that actually answered", probe.Edge)
	}
}
