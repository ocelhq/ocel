package providerkit

import (
	"context"
	"errors"
	"strings"
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

type boxProvider struct {
	Provider
	answers edge.Kind
	asked   []string
	kinds   []edge.Kind
}

func (p *boxProvider) Serving(_ context.Context, kind edge.Kind, hostname string) (edge.Kind, error) {
	p.asked = append(p.asked, hostname)
	p.kinds = append(p.kinds, kind)
	return p.answers, nil
}

type frontOf struct {
	edge.Edge
	kind edge.Kind
}

func (f frontOf) Kind() edge.Kind { return f.kind }

func (f frontOf) Facts() edge.Facts { return edge.Facts{} }

func TestAProviderThatProbesSuppliesTheResolverInsteadOfTheStandIn(t *testing.T) {
	t.Parallel()

	front := frontOf{kind: "box"}
	stand := &answering{kind: "box"}
	provider := &boxProvider{answers: "box"}

	if resolve := resolving(nil, front, stand); resolve != Resolver(stand) {
		t.Errorf("a provider that probes nothing = %T, want the stand-in the kit falls back to", resolve)
	}
	resolve := resolving(provider, front, stand)
	if kind, err := resolve.Serving(context.Background(), "shop.example.com"); err != nil || kind != "box" {
		t.Fatalf("Serving() = %q, %v, want the edge the provider's own probe answered", kind, err)
	}
	if stand.asked != 0 {
		t.Error("the stand-in was asked as well, and it answers from what the kit just bound rather than from what the hostname serves")
	}
	if len(provider.asked) != 1 || provider.asked[0] != "shop.example.com" || provider.kinds[0] != "box" {
		t.Errorf("the provider was asked %v as %v, want the hostname being settled and the edge it is settled onto", provider.asked, provider.kinds)
	}
}

func TestTheSettleRefusesAHostnameAnotherEdgeAnswersOn(t *testing.T) {
	t.Parallel()

	provider := &boxProvider{answers: "cloudfront"}
	settle, _ := waiting(resolving(provider, frontOf{kind: "box"}, &answering{kind: "box"}), 3)
	settle.kind = "box"

	probe, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeNotReady {
		t.Fatalf("await() over a hostname a different edge answers = %v, want a not-ready refusal rather than a settle that reads as done", err)
	}
	if !strings.Contains(refusal.Message, "cloudfront") || !strings.Contains(refusal.Message, "box") {
		t.Errorf("await() refused with %q, want it to name the edge answering and the one this project deploys to", refusal.Message)
	}
	if probe.OK || probe.Edge != "cloudfront" {
		t.Errorf("await() = %+v, want the probe to record the edge that actually answered", probe)
	}
	if len(provider.asked) != 3 {
		t.Errorf("the provider's probe was asked %d time(s), want every attempt the settle makes", len(provider.asked))
	}
}

type slowly struct {
	clock *time.Time
	cost  time.Duration
}

func (s slowly) Serving(context.Context, string) (edge.Kind, error) {
	*s.clock = s.clock.Add(s.cost)
	return "", nil
}

func TestTheSettleReportsTheTimeItSpentRatherThanTheTimeItPlannedTo(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1700000000, 0)
	settle := settler{
		kind:     "box",
		resolve:  slowly{clock: &clock, cost: 15 * time.Second},
		attempts: 4,
		wait:     5 * time.Second,
		now:      func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
	}

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "1m15s") {
		t.Errorf("await() refused with %q, want the 1m15s it actually spent: a resolver that reaches the hostname costs a request per attempt, and the sleeps between them are no longer the whole of the wait",
			refusal.Message)
	}
}
