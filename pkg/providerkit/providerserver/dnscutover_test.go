package providerserver

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type answering struct {
	kind  edge.Kind
	after int
	asked int
}

func (*answering) LastProbeFailure(string) string { return "" }

func (a *answering) ServingEdge(context.Context, edge.Kind, string) (edge.Kind, error) {
	a.asked++
	if a.asked > a.after {
		return a.kind, nil
	}
	return "", nil
}

func waiting(liveness provider.Liveness, attempts int) (settlement, *int) {
	slept := 0
	clock := time.Unix(1700000000, 0)
	return settlement{
		kind:     "relay",
		liveness: liveness,
		budget:   time.Duration(attempts) * time.Second,
		window:   time.Second,
		wait:     time.Second,
		now:      func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error {
			slept++
			clock = clock.Add(d)
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
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
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
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if probe.Edge != "direct" {
		t.Errorf("await() recorded %q, want the edge that actually answered", probe.Edge)
	}
}

type boxProvider struct {
	provider.Provider
	answers edge.Kind
	asked   []string
	kinds   []edge.Kind
}

func (p *boxProvider) Liveness() provider.Liveness { return p }

func (*boxProvider) LastProbeFailure(string) string { return "" }

func (p *boxProvider) ServingEdge(_ context.Context, kind edge.Kind, hostname string) (edge.Kind, error) {
	p.asked = append(p.asked, hostname)
	p.kinds = append(p.kinds, kind)
	return p.answers, nil
}

type diagnosingProvider struct {
	*boxProvider
	cause string
}

func (p diagnosingProvider) Liveness() provider.Liveness { return p }

func (p diagnosingProvider) LastProbeFailure(string) string { return p.cause }

type frontOf struct {
	edge.Edge
	kind edge.Kind
}

func (f frontOf) Kind() edge.Kind { return f.kind }

func (f frontOf) Facts() edge.Facts { return edge.Facts{} }

func TestTheSettleAsksTheProvidersProbeWhichEdgeAnswers(t *testing.T) {
	t.Parallel()

	provider := &boxProvider{answers: "box"}
	settle := newSettlement(frontOf{kind: "box"}, nil, "", provider.Liveness())
	if kind, err := settle.attempt(context.Background(), "shop.example.com"); err != nil || kind != "box" {
		t.Fatalf("attempt() = %q, %v, want the edge the provider's own probe answered", kind, err)
	}
	if len(provider.asked) != 1 || provider.asked[0] != "shop.example.com" || provider.kinds[0] != "box" {
		t.Errorf("the provider was asked %v as %v, want the hostname being settled and the edge it is settled onto", provider.asked, provider.kinds)
	}
}

func TestTheSettleRefusesAHostnameAnotherEdgeAnswersOn(t *testing.T) {
	t.Parallel()

	provider := &boxProvider{answers: "cloudfront"}
	settle, _ := waiting(provider.Liveness(), 3)
	settle.kind = "box"

	probe, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() over a hostname a different edge answers = %v, want a not-ready refusal rather than a settle that reads as done", err)
	}
	if !strings.Contains(refused.Message, "cloudfront") || !strings.Contains(refused.Message, "box") {
		t.Errorf("await() refused with %q, want it to name the edge answering and the one this project deploys to", refused.Message)
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
	asked *int
}

func (slowly) LastProbeFailure(string) string { return "" }

func (s slowly) ServingEdge(context.Context, edge.Kind, string) (edge.Kind, error) {
	*s.asked++
	*s.clock = s.clock.Add(s.cost)
	return "", nil
}

func onTheClock(resolve func(*time.Time) provider.Liveness, budget time.Duration) settlement {
	clock := time.Unix(1700000000, 0)
	return settlement{
		kind:     "box",
		liveness: resolve(&clock),
		budget:   budget,
		window:   attemptWindow,
		wait:     5 * time.Second,
		now:      func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
	}
}

func TestADeployGivesUpOnceItsMinuteHasPassedHoweverLongEachAttemptTakes(t *testing.T) {
	t.Parallel()

	var asked int
	settle := onTheClock(func(clock *time.Time) provider.Liveness {
		return slowly{clock: clock, cost: 15 * time.Second, asked: &asked}
	}, settleBudget)

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refusal refusal.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if asked != 3 {
		t.Errorf("await() asked %d time(s), want the 3 that begin inside the minute: attempts at 0s, 20s and 40s each cost 15s, and a fourth at 60s would start past the bound the deploy reports", asked)
	}
	if !strings.Contains(refusal.Message, "55s") {
		t.Errorf("await() refused with %q, want the 55s it actually spent", refusal.Message)
	}
}

func TestAnAttendedSettleWaitsOutAFrontThatTakesMinutesToAnswer(t *testing.T) {
	t.Parallel()

	const minutes = 10 * time.Minute
	for what, budget := range map[string]time.Duration{"domain add": attendedBudget, "a deploy": settleBudget} {
		settle := onTheClock(func(clock *time.Time) provider.Liveness {
			return answeringAfter{clock: clock, at: clock.Add(minutes), kind: "box"}
		}, budget)
		_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
		if budget == attendedBudget && err != nil {
			t.Errorf("%s gave up on a front that answers after %s: %v. A fresh CloudFront distribution takes minutes to serve, and `ocel domain add` is the command that waits for it", what, minutes, err)
		}
		if budget == settleBudget && err == nil {
			t.Errorf("%s waited %s for one hostname: a deploy leaves a slow hostname pending for `ocel domain add` rather than holding the release", what, minutes)
		}
	}
}

type answeringAfter struct {
	clock *time.Time
	at    time.Time
	kind  edge.Kind
}

func (answeringAfter) LastProbeFailure(string) string { return "" }

func (a answeringAfter) ServingEdge(context.Context, edge.Kind, string) (edge.Kind, error) {
	if a.clock.Before(a.at) {
		return "", nil
	}
	return a.kind, nil
}

type hanging struct{ asked *atomic.Int32 }

func (hanging) LastProbeFailure(string) string { return "" }

func (h hanging) ServingEdge(ctx context.Context, _ edge.Kind, _ string) (edge.Kind, error) {
	h.asked.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestAProbeThatNeverReturnsIsCutOffAtEachAttemptAndTheSettleAtItsDeadline(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	settle := settlement{
		kind:     "box",
		liveness: hanging{asked: &asked},
		budget:   300 * time.Millisecond,
		window:   50 * time.Millisecond,
		wait:     10 * time.Millisecond,
		now:      time.Now,
		sleep:    sleep,
	}

	began := time.Now()
	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	if spent := time.Since(began); spent > 2*time.Second {
		t.Fatalf("await() returned after %s, want it held to its 300ms deadline", spent)
	}
	if _, held := provider.LeftPending(err); !held {
		t.Fatalf("await() = %v, want the hostname left pending when its wait runs out, not the run failed", err)
	}
	if asked.Load() < 2 {
		t.Errorf("the probe was asked %d time(s), want each hung attempt cut off so the next one runs", asked.Load())
	}
	if !strings.Contains(err.Error(), "50ms") {
		t.Errorf("await() said %q, want it to name the attempt that outlasted its window", err)
	}
}

type stopped struct {
	cause string
}

func (stopped) ServingEdge(context.Context, edge.Kind, string) (edge.Kind, error) { return "", nil }

func (s stopped) LastProbeFailure(string) string { return s.cause }

func TestAHostnameNothingAnsweredForNamesWhatStoppedTheLastAttempt(t *testing.T) {
	t.Parallel()

	cause := "dial tcp 203.0.113.10:443: connect: connection refused"
	settle, _ := waiting(stopped{cause: cause}, 2)

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(refused.Message, cause) {
		t.Errorf("await() refused with %q, and it never says what stopped the probe: a firewalled port and a certificate that has not been issued yet read identically after a full minute of waiting",
			refused.Message)
	}
}

type outlasting struct {
	cause string
}

func (outlasting) ServingEdge(context.Context, edge.Kind, string) (edge.Kind, error) {
	return "", context.DeadlineExceeded
}

func (o outlasting) LastProbeFailure(string) string { return o.cause }

func TestAHostnameWhoseLastAttemptOutlastedItsWindowStillNamesWhatStoppedIt(t *testing.T) {
	t.Parallel()

	cause := "dial tcp 203.0.113.10:443: i/o timeout"
	settle, _ := waiting(outlasting{cause: cause}, 2)

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(refused.Message, cause) {
		t.Errorf("await() refused with %q, and the window it outlasted crowds out what stopped the probe", refused.Message)
	}
	if !strings.Contains(refused.Message, "no answer within 1s") {
		t.Errorf("await() refused with %q, want it to name the window the last attempt outlasted", refused.Message)
	}
}

func TestALivenessThatDiagnosesNothingStillRefusesInOneSentence(t *testing.T) {
	t.Parallel()

	settle, _ := waiting(&answering{kind: "relay", after: 99}, 2)

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refusal refusal.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if strings.Contains(refusal.Message, "ended in") {
		t.Errorf("await() refused with %q, want no dangling clause where a resolver carries no cause to name", refusal.Message)
	}
}

func TestAProviderThatDiagnosesItsOwnProbeIsAskedThroughItsLiveness(t *testing.T) {
	t.Parallel()

	cause := "x509: certificate is valid for parked.example.net, not shop.example.com"
	provider := diagnosingProvider{boxProvider: &boxProvider{}, cause: cause}
	settle, _ := waiting(provider.Liveness(), 2)
	settle.kind = "box"

	_, err := settle.await(context.Background(), "shop.example.com", func(string) {})
	var refusal refusal.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, cause) {
		t.Errorf("await() refused with %q, and the cause the provider recorded never reaches the operator", refusal.Message)
	}
}

func TestAStatusLineForAHostnameThatWillNotAnswerNamesWhatStoppedTheProbe(t *testing.T) {
	t.Parallel()

	cause := "x509: certificate signed by unknown authority"
	settle, _ := waiting(stopped{cause: cause}, 1)
	settle.kind = "box"
	rows := &hostnames{
		edgeSession: &edgeSession{settle: settle},
		configured:  []ConfiguredHost{{Hostname: "shop.example.com"}},
	}

	probe := rows.probe(context.Background(), "shop.example.com")
	if probe.OK {
		t.Fatal("a hostname nothing answered for probed OK, and this test states nothing about the line it is reported under")
	}
	pending := rows.pendingOn("shop.example.com", provider.Certificate{}, provider.CertificateHealth{}, true, probe)
	if !strings.Contains(pending, cause) {
		t.Errorf("`domain status` reports %q, and never what stopped the probe: the operator is told the hostname does not answer yet and left to guess between a firewall, a record that has not propagated and a chain nothing trusts",
			pending)
	}
}
