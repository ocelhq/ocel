package providerserver

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type answering struct {
	kind  router.Kind
	after int
	asked int
}

func (*answering) LastProbeFailure(string) string { return "" }

func (a *answering) ServingRouter(context.Context, string) (router.Kind, error) {
	a.asked++
	if a.asked > a.after {
		return a.kind, nil
	}
	return "", nil
}

func waiting(liveness provider.Liveness, attempts int) (dnsCutover, *int) {
	slept := 0
	clock := time.Unix(1700000000, 0)
	return dnsCutover{
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

func TestCutoverWaitsUntilTheHostnameAnswers(t *testing.T) {
	t.Parallel()
	resolve := &answering{kind: "relay", after: 2}
	cutover, slept := waiting(resolve, 5)

	probe, err := cutover.await(context.Background(), "app.acme.com", "relay", func(string) {})
	if err != nil {
		t.Fatalf("await() = %v, want it to wait the hostname out", err)
	}
	if !probe.OK || probe.Router != "relay" {
		t.Errorf("await() = %+v, want a probe answered by the relay router", probe)
	}
	if *slept != 2 {
		t.Errorf("await() slept %d time(s), want the two it waited through", *slept)
	}
}

func TestCutoverGivesUpAfterItsLastAttempt(t *testing.T) {
	t.Parallel()
	resolve := &answering{kind: "relay", after: 99}
	cutover, slept := waiting(resolve, 3)

	probe, err := cutover.await(context.Background(), "app.acme.com", "relay", func(string) {})
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

func TestCutoverStopsWhenAnotherEdgeAnswers(t *testing.T) {
	t.Parallel()
	cutover, _ := waiting(&answering{kind: "direct"}, 2)

	probe, err := cutover.await(context.Background(), "app.acme.com", "relay", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if probe.Router != "direct" {
		t.Errorf("await() recorded %q, want the router that actually answered", probe.Router)
	}
}

type boxProvider struct {
	provider.Provider
	answers router.Kind
	asked   []string
}

func (p *boxProvider) Liveness() provider.Liveness { return p }

func (*boxProvider) LastProbeFailure(string) string { return "" }

func (p *boxProvider) ServingRouter(_ context.Context, hostname string) (router.Kind, error) {
	p.asked = append(p.asked, hostname)
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

func TestTheCutoverAsksTheProvidersProbeWhichRouterAnswers(t *testing.T) {
	t.Parallel()

	vendor := &boxProvider{answers: "switchboard"}
	cutover := newDNSCutover(frontOf{kind: edge.None}, nil, "", vendor.Liveness())
	if kind, err := cutover.attempt(context.Background(), "shop.example.com"); err != nil || kind != "switchboard" {
		t.Fatalf("attempt() = %q, %v, want the router the provider's own probe answered", kind, err)
	}
	if len(vendor.asked) != 1 || vendor.asked[0] != "shop.example.com" {
		t.Errorf("the provider was asked %v, want the hostname being cut over", vendor.asked)
	}
}

func TestTheCutoverRefusesAHostnameAnotherRouterAnswersOnWithoutNamingEither(t *testing.T) {
	t.Parallel()

	vendor := &boxProvider{answers: "cloudfront"}
	cutover, _ := waiting(vendor.Liveness(), 3)
	cutover.kind = edge.None

	probe, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() over a hostname a different router answers = %v, want a not-ready refusal rather than a cutover that reads as done", err)
	}
	if !strings.Contains(refused.Message, "another front") || !strings.Contains(refused.Message, "the origin") {
		t.Errorf("await() refused with %q, want it to say another front answers, not the origin this project deploys to", refused.Message)
	}
	if strings.Contains(refused.Message, "cloudfront") || strings.Contains(refused.Message, "switchboard") {
		t.Errorf("await() refused with %q; a router is never user-facing, so no refusal names one", refused.Message)
	}
	if probe.OK || probe.Router != "cloudfront" {
		t.Errorf("await() = %+v, want the probe to record the router that actually answered", probe)
	}
	if len(vendor.asked) != 3 {
		t.Errorf("the provider's probe was asked %d time(s), want every attempt the cutover makes", len(vendor.asked))
	}
}

type slowly struct {
	clock *time.Time
	cost  time.Duration
	asked *int
}

func (slowly) LastProbeFailure(string) string { return "" }

func (s slowly) ServingRouter(context.Context, string) (router.Kind, error) {
	*s.asked++
	*s.clock = s.clock.Add(s.cost)
	return "", nil
}

func onTheClock(resolve func(*time.Time) provider.Liveness, budget time.Duration) dnsCutover {
	clock := time.Unix(1700000000, 0)
	return dnsCutover{
		kind:     edge.None,
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
	cutover := onTheClock(func(clock *time.Time) provider.Liveness {
		return slowly{clock: clock, cost: 15 * time.Second, asked: &asked}
	}, cutoverBudget)

	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if asked != 3 {
		t.Errorf("await() asked %d time(s), want the 3 that begin inside the minute: attempts at 0s, 20s and 40s each cost 15s, and a fourth at 60s would start past the bound the deploy reports", asked)
	}
	if !strings.Contains(refused.Message, "55s") {
		t.Errorf("await() refused with %q, want the 55s it actually spent", refused.Message)
	}
}

func TestAnAttendedCutoverWaitsOutAFrontThatTakesMinutesToAnswer(t *testing.T) {
	t.Parallel()

	const minutes = 10 * time.Minute
	for what, budget := range map[string]time.Duration{"domain add": manualRecordBudget, "a deploy": cutoverBudget} {
		cutover := onTheClock(func(clock *time.Time) provider.Liveness {
			return answeringAfter{clock: clock, at: clock.Add(minutes), kind: "switchboard"}
		}, budget)
		_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
		if budget == manualRecordBudget && err != nil {
			t.Errorf("%s gave up on a front that answers after %s: %v. A fresh CloudFront distribution takes minutes to serve, and `ocel domain add` is the command that waits for it", what, minutes, err)
		}
		if budget == cutoverBudget && err == nil {
			t.Errorf("%s waited %s for one hostname: a deploy leaves a slow hostname pending for `ocel domain add` rather than delaying the release", what, minutes)
		}
	}
}

type answeringAfter struct {
	clock *time.Time
	at    time.Time
	kind  router.Kind
}

func (answeringAfter) LastProbeFailure(string) string { return "" }

func (a answeringAfter) ServingRouter(context.Context, string) (router.Kind, error) {
	if a.clock.Before(a.at) {
		return "", nil
	}
	return a.kind, nil
}

type hanging struct{ asked *atomic.Int32 }

func (hanging) LastProbeFailure(string) string { return "" }

func (h hanging) ServingRouter(ctx context.Context, _ string) (router.Kind, error) {
	h.asked.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestAProbeThatNeverReturnsIsCutOffAtEachAttemptAndTheCutoverAtItsDeadline(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	cutover := dnsCutover{
		kind:     edge.None,
		liveness: hanging{asked: &asked},
		budget:   300 * time.Millisecond,
		window:   50 * time.Millisecond,
		wait:     10 * time.Millisecond,
		now:      time.Now,
		sleep:    sleep,
	}

	began := time.Now()
	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	if spent := time.Since(began); spent > 2*time.Second {
		t.Fatalf("await() returned after %s, want it bounded by its 300ms deadline", spent)
	}
	if _, ok := provider.ResumableMessage(err); !ok {
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

func (stopped) ServingRouter(context.Context, string) (router.Kind, error) { return "", nil }

func (s stopped) LastProbeFailure(string) string { return s.cause }

func TestAHostnameNothingAnsweredForNamesWhatStoppedTheLastAttempt(t *testing.T) {
	t.Parallel()

	cause := "dial tcp 203.0.113.10:443: connect: connection refused"
	cutover, _ := waiting(stopped{cause: cause}, 2)

	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
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

func (outlasting) ServingRouter(context.Context, string) (router.Kind, error) {
	return "", context.DeadlineExceeded
}

func (o outlasting) LastProbeFailure(string) string { return o.cause }

func TestAHostnameWhoseLastAttemptOutlastedItsWindowStillNamesWhatStoppedIt(t *testing.T) {
	t.Parallel()

	cause := "dial tcp 203.0.113.10:443: i/o timeout"
	cutover, _ := waiting(outlasting{cause: cause}, 2)

	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("await() = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(refused.Message, cause) {
		t.Errorf("await() refused with %q, and the window it outlasted crowds out what stopped the probe", refused.Message)
	}
	if !strings.Contains(refused.Message, "ended in: "+cause+", and it got no answer within 1s") {
		t.Errorf("await() refused with %q, want it to name the window the last attempt outlasted", refused.Message)
	}
}

func TestALivenessThatDiagnosesNothingStillRefusesInOneSentence(t *testing.T) {
	t.Parallel()

	cutover, _ := waiting(&answering{kind: "relay", after: 99}, 2)

	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if strings.Contains(refused.Message, "ended in") {
		t.Errorf("await() refused with %q, want no dangling clause where a resolver gives no cause to name", refused.Message)
	}
}

func TestAProviderThatDiagnosesItsOwnProbeIsAskedThroughItsLiveness(t *testing.T) {
	t.Parallel()

	cause := "x509: certificate is valid for parked.example.net, not shop.example.com"
	vendor := diagnosingProvider{boxProvider: &boxProvider{}, cause: cause}
	cutover, _ := waiting(vendor.Liveness(), 2)
	cutover.kind = edge.None

	_, err := cutover.await(context.Background(), "shop.example.com", "switchboard", func(string) {})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("await() = %v, want a refusal", err)
	}
	if !strings.Contains(refused.Message, cause) {
		t.Errorf("await() refused with %q, and the cause the provider recorded never reaches the operator", refused.Message)
	}
}

func TestAStatusLineForAHostnameThatWillNotAnswerNamesWhatStoppedTheProbe(t *testing.T) {
	t.Parallel()

	cause := "x509: certificate signed by unknown authority"
	cutover, _ := waiting(stopped{cause: cause}, 1)
	cutover.kind = edge.None
	rows := &hostnames{
		edgeSession: &edgeSession{cutover: cutover},
		configured:  []ConfiguredHost{{Hostname: "shop.example.com"}},
	}

	probe := rows.probe(context.Background(), "shop.example.com", "switchboard")
	if probe.OK {
		t.Fatal("a hostname nothing answered for probed OK, and this test states nothing about the line it is reported under")
	}
	pending := rows.hostnameBlocker("shop.example.com", provider.Certificate{}, provider.CertificateHealth{}, true, probe)
	if !strings.Contains(pending, cause) {
		t.Errorf("`domain status` reports %q, and never what stopped the probe: the operator is told the hostname does not answer yet and left to guess between a firewall, a record that has not propagated and a chain nothing trusts",
			pending)
	}
}
