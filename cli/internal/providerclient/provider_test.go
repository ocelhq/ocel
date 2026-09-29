package providerclient

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type recording struct {
	mu     sync.Mutex
	events []*streamv1.RunEvent
}

func (r *recording) Receive(ev *streamv1.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recording) Close() error { return nil }

func (r *recording) received() []*streamv1.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*streamv1.RunEvent(nil), r.events...)
}

func deployScope(t *testing.T) (context.Context, *events.Scope, *recording) {
	t.Helper()

	seen := &recording{}
	bus := events.NewBus(time.Now)
	bus.Attach(seen)
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	return ctx, run.Phase(progressv1.Phase_PHASE_DEPLOY), seen
}

func startFake(t *testing.T, ctx context.Context, mode string, scope *events.Scope, trust Trust, env ...string) *Provider {
	t.Helper()

	p, err := start(ctx, scope, trust, fakeConfig(t, mode, Config{ProviderName: "fake", Env: env}))
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestAStartedProvidersStreamEventsReachTheScope(t *testing.T) {
	t.Parallel()

	ctx, scope, seen := deployScope(t)
	p := startFake(t, ctx, "success", scope, Trust{})

	result, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"},
	}, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the provider's successful result returned", result.GetError())
	}

	var said, outcome bool
	for _, ev := range seen.received() {
		said = said || ev.GetMessage() == "step 1"
		outcome = outcome || ev.GetResult().GetSuccess()
	}
	if !said {
		t.Error("the scope never saw the provider's \"step 1\" line")
	}
	if !outcome {
		t.Error("the scope never saw the provider's result as the run's outcome")
	}
}

func TestAPlanningStreamHandsBackThePlanAndForwardsEveryOtherEvent(t *testing.T) {
	t.Parallel()

	ctx, scope, seen := deployScope(t)
	p := startFake(t, ctx, "success", scope, Trust{})

	plan, err := Plan(ctx, p, "Deploy", &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"},
		Dry:      true,
	}, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !proto.Equal(plan, fakePlan) {
		t.Errorf("plan = %v, want the plan the provider streamed", plan)
	}

	var said bool
	for _, ev := range seen.received() {
		said = said || ev.GetMessage() == "step 1"
		if ev.GetPlan() != nil {
			t.Error("the scope saw the plan, want it handed back for the command to draw")
		}
	}
	if !said {
		t.Error("the scope never saw the provider's \"step 1\" line")
	}
}

func TestALineTheProviderWritesToStderrReachesTheRunAtDebugNamingTheProviderInThePhaseItStartedIn(t *testing.T) {
	t.Parallel()

	ctx, scope, seen := deployScope(t)
	p := startFake(t, ctx, "chatty", scope, Trust{})
	p.Close()

	for _, ev := range seen.received() {
		if ev.GetMessage() != fakeChattyLine {
			continue
		}
		if ev.GetLevel() != progressv1.Level_LEVEL_DEBUG || ev.GetSubject() != "fake" || ev.GetOutput().GetStream() != progressv1.Stream_STREAM_STDERR ||
			ev.GetPhase() != progressv1.Phase_PHASE_DEPLOY {
			t.Errorf("the stderr line arrived as %s from %q on %s in %s, want DEBUG output from \"fake\" on stderr in the deploy phase it started in",
				ev.GetLevel(), ev.GetSubject(), ev.GetOutput().GetStream(), ev.GetPhase())
		}
		return
	}
	t.Errorf("the scope never saw the provider's stderr line %q", fakeChattyLine)
}

func bodies(seen *recording) []string {
	var kinds []string
	for _, ev := range seen.received() {
		switch {
		case ev.GetWaiting() != nil:
			kinds = append(kinds, "waiting")
		case ev.GetResumed() != nil:
			kinds = append(kinds, "resumed "+ev.GetResumed().GetReason())
		}
	}
	return kinds
}

func TestAnUnknownHostKeyOnAStreamIsAskedOnceUnderAHoldRecordedAndThatCallRetriedOnAFreshProcess(t *testing.T) {
	t.Parallel()

	ctx, scope, seen := deployScope(t)
	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedAsker{attended: true, answer: true}
	var out bytes.Buffer
	p := startFake(t, ctx, "unknown-host-key", scope, trustAsking(asker, &out), fake.env()...)

	result, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
	if err != nil {
		t.Fatalf("Stream() error = %v, want the retried call to succeed", err)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the retried call's success", result.GetError())
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times (%v), want exactly one prompt", len(asker.asked), asker.asked)
	}
	if got := fake.recorded(t); got != wantedLine() {
		t.Errorf("known_hosts = %q, want %q", got, wantedLine())
	}
	if !strings.Contains(out.String(), fakeKey(fakeHostKey).Fingerprint) {
		t.Errorf("output = %q, want the fingerprint shown before asking", out.String())
	}
	drivenBy := fake.drivenBy(t)
	if len(drivenBy) != 2 || drivenBy[0] == drivenBy[1] {
		t.Errorf("the call ran in processes %v, want it once and then once more in a restarted provider", drivenBy)
	}
	if got := bodies(seen); !slices.Equal(got, []string{"waiting", "resumed answered"}) {
		t.Errorf("the scope saw %v around the prompt, want the run held while it asked", got)
	}
}

func TestAHostKeyPromptAfterTheStartingPhaseEndedHoldsTheRunNotThatPhase(t *testing.T) {
	t.Parallel()

	seen := &recording{}
	bus := events.NewBus(time.Now)
	bus.Attach(seen)
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	fake := newHostTrustFake(t, "unknown-host-key")
	p := startFake(t, ctx, "unknown-host-key", check, trustAsking(&scriptedAsker{attended: true, answer: true}, io.Discard), fake.env()...)
	check.End(nil)

	if _, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap); err != nil {
		t.Fatalf("Stream() error = %v, want the retried call to succeed", err)
	}

	held := 0
	for _, ev := range seen.received() {
		if ev.GetWaiting() == nil && ev.GetResumed() == nil {
			continue
		}
		held++
		if ev.GetPhase() != progressv1.Phase_PHASE_UNSPECIFIED || len(ev.GetSpanId()) != 0 {
			t.Errorf("the prompt's hold arrived in %s on span %x, want it on the run, not the check phase that had ended", ev.GetPhase(), ev.GetSpanId())
		}
	}
	if held != 2 {
		t.Errorf("saw %d hold events, want waiting then resumed", held)
	}
}

func TestAnUnknownHostKeyOnAUnaryCallIsAskedOnceRecordedAndThatCallRetried(t *testing.T) {
	t.Parallel()

	ctx, scope, _ := deployScope(t)
	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedAsker{attended: true, answer: true}
	p := startFake(t, ctx, "unknown-host-key", scope, trustAsking(asker, io.Discard), fake.env()...)

	err := p.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.Preflight(ctx, &contractv1.PreflightRequest{})
		return err
	})
	if err != nil {
		t.Fatalf("Call() error = %v, want the retried call to succeed", err)
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times (%v), want exactly one prompt", len(asker.asked), asker.asked)
	}
	if got := fake.drivenTimes(t); got != 2 {
		t.Errorf("the call ran %d times, want 2", got)
	}
	if got := fake.recorded(t); got != wantedLine() {
		t.Errorf("known_hosts = %q, want %q", got, wantedLine())
	}
}

func TestAHostKeyRefusedAtThePromptLeavesTheCallsErrorStandingAndRetriesNothing(t *testing.T) {
	t.Parallel()

	ctx, scope, seen := deployScope(t)
	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedAsker{attended: true, answer: false}
	p := startFake(t, ctx, "unknown-host-key", scope, trustAsking(asker, io.Discard), fake.env()...)

	_, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
	if trust, ok := provider.HostTrustOf(err); !ok || trust.Reason != provider.UnknownHostKey {
		t.Errorf("Stream() error = %v, want the unknown-host-key refusal", err)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := bodies(seen); !slices.Equal(got, []string{"waiting", "resumed answered"}) {
		t.Errorf("the scope saw %v around the prompt, want the run held while it asked", got)
	}
}
