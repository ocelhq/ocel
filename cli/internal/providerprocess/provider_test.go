package providerprocess

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/confirmation"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/jobinput"
	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

type recording struct {
	mu     sync.Mutex
	events []*streamv1.RunEvent
}

func (r *recording) Receive(event *streamv1.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recording) Close() error { return nil }

func (r *recording) received() []*streamv1.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*streamv1.RunEvent(nil), r.events...)
}

func deploySpan(t *testing.T) (context.Context, *run.Span, *recording) {
	t.Helper()

	seen := &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(seen)
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	return ctx, run.Phase(progressv1.Phase_PHASE_DEPLOY), seen
}

func startFake(t *testing.T, ctx context.Context, mode string, span *run.Span, questions Questions, env ...string) *Provider {
	t.Helper()

	p, err := start(ctx, span, questions, fakeSpec(t, mode, LaunchSpec{ProviderName: "fake", Env: env}))
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestASpanAProviderFlushesAsItIsTornDownAfterItsRunEndsIsInTheRunsTrace(t *testing.T) {
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	p := startFake(t, ctx, "flushes-span-on-exit", traced.Phase(progressv1.Phase_PHASE_DEPLOY), Questions{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		p.Close()
	}()
	traced.End(&err)

	spans := tracedSpans(t, tracePathOf(t, dir, traced))
	flushed, kept := spans["flushed on exit"]
	if !kept {
		t.Fatal("the trace lost the span the provider flushed as it was torn down")
	}
	if process := spans[filepath.Base(os.Args[0])]; process.SpanID == "" || flushed.ParentSpanID != process.SpanID {
		t.Errorf("the provider's span has parent %q, want its process span %q", flushed.ParentSpanID, process.SpanID)
	}
}

func TestAStartedProvidersStreamEventsReachTheSpan(t *testing.T) {
	t.Parallel()

	ctx, span, seen := deploySpan(t)
	p := startFake(t, ctx, "success", span, Questions{})

	result, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{Slug: "acme"},
	}, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the provider's successful result returned", result.GetError())
	}

	var said, outcome bool
	for _, event := range seen.received() {
		said = said || event.GetOperation().GetMessage() == "step 1"
		outcome = outcome || event.GetOperation().GetResult().GetSuccess()
	}
	if !said {
		t.Error("the span never saw the provider's \"step 1\" line")
	}
	if !outcome {
		t.Error("the span never saw the provider's result as the run's outcome")
	}
}

func TestACleanupCallMadeAfterTheRunIsInterruptedStillReachesTheProvider(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	interrupted, interrupt := context.WithCancel(ctx)
	p := startFake(t, interrupted, "success", span, Questions{})
	interrupt()

	select {
	case <-p.transport.(*Process).done:
		t.Fatal("the provider exited when the run was interrupted, before its deferred cleanup could reach it")
	case <-time.After(500 * time.Millisecond):
	}
	err := p.Call(context.WithoutCancel(interrupted), func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.DescribeBootstrap(context.WithoutCancel(interrupted), &contractv1.DescribeBootstrapRequest{})
		return err
	})
	if err != nil {
		t.Fatalf("a cleanup call after the interrupt = %v, want it served by the provider that is still open", err)
	}
}

func TestAPlanningStreamHandsBackThePlanAndForwardsEveryOtherEvent(t *testing.T) {
	t.Parallel()

	ctx, span, seen := deploySpan(t)
	p := startFake(t, ctx, "success", span, Questions{})

	plan, err := Plan(ctx, p, "Deploy", &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{Slug: "acme"},
		Dry:      true,
	}, contractv1connect.ProviderServiceClient.Deploy)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !proto.Equal(plan, fakePlan) {
		t.Errorf("plan = %v, want the plan the provider streamed", plan)
	}

	var said bool
	for _, event := range seen.received() {
		said = said || event.GetOperation().GetMessage() == "step 1"
		if event.GetOperation().GetPlan() != nil {
			t.Error("the span saw the plan, want it handed back for the command to draw")
		}
	}
	if !said {
		t.Error("the span never saw the provider's \"step 1\" line")
	}
}

func TestALineTheProviderWritesToStderrReachesTheRunAtDebugNamingTheProviderInThePhaseItStartedIn(t *testing.T) {
	t.Parallel()

	ctx, span, seen := deploySpan(t)
	p := startFake(t, ctx, "chatty", span, Questions{})
	p.Close()

	for _, event := range seen.received() {
		if event.GetOperation().GetMessage() != fakeChattyLine {
			continue
		}
		if event.GetOperation().GetLevel() != progressv1.Level_LEVEL_DEBUG || event.GetOperation().GetSubject() != "fake" || event.GetOperation().GetOutput().GetStream() != progressv1.Stream_STREAM_STDERR ||
			event.GetOperation().GetPhase() != progressv1.Phase_PHASE_DEPLOY {
			t.Errorf("the stderr line arrived as %s from %q on %s in %s, want DEBUG output from \"fake\" on stderr in the deploy phase it started in",
				event.GetOperation().GetLevel(), event.GetOperation().GetSubject(), event.GetOperation().GetOutput().GetStream(), event.GetOperation().GetPhase())
		}
		return
	}
	t.Errorf("the span never saw the provider's stderr line %q", fakeChattyLine)
}

func bodies(seen *recording) []string {
	var kinds []string
	for _, event := range seen.received() {
		switch {
		case event.GetWaiting() != nil:
			kinds = append(kinds, "waiting")
		case event.GetResumed() != nil:
			kinds = append(kinds, "resumed "+event.GetResumed().GetReason())
		}
	}
	return kinds
}

func TestAQuestionOnAStreamIsAskedOnceUnderAHoldConfirmedAndThatCallRetried(t *testing.T) {
	t.Parallel()

	ctx, span, seen := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}
	var out bytes.Buffer
	p := startFake(t, ctx, "unknown-host-key", span, answering(asker, &out), fake.env()...)

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
	if got := fake.recorded(t); got != fakeHostLine() {
		t.Errorf("known_hosts = %q, want %q", got, fakeHostLine())
	}
	if !strings.Contains(out.String(), fakeHostFingerprint) {
		t.Errorf("output = %q, want the fingerprint shown before asking", out.String())
	}
	drivenBy := fake.drivenBy(t)
	if len(drivenBy) != 2 || drivenBy[0] != drivenBy[1] {
		t.Errorf("the call ran in processes %v, want it twice in the one provider that asked", drivenBy)
	}
	if got := bodies(seen); !slices.Equal(got, []string{"waiting", "resumed answered"}) {
		t.Errorf("the span saw %v around the prompt, want the run held while it asked", got)
	}
}

func TestAQuestionAskedAfterTheStartingPhaseEndedHoldsTheRunNotThatPhase(t *testing.T) {
	t.Parallel()

	seen := &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(seen)
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	fake := newQuestionFake(t, "unknown-host-key")
	p := startFake(t, ctx, "unknown-host-key", check, answering(&scriptedPrompt{attended: true, answer: true}, io.Discard), fake.env()...)
	check.End(nil)

	if _, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap); err != nil {
		t.Fatalf("Stream() error = %v, want the retried call to succeed", err)
	}

	held := 0
	for _, event := range seen.received() {
		if event.GetWaiting() == nil && event.GetResumed() == nil {
			continue
		}
		held++
		if event.GetOperation().GetPhase() != progressv1.Phase_PHASE_UNSPECIFIED || len(event.GetOperation().GetSpanId()) != 0 {
			t.Errorf("the prompt's hold arrived in %s on span %x, want it on the run, not the check phase that had ended", event.GetOperation().GetPhase(), event.GetOperation().GetSpanId())
		}
	}
	if held != 2 {
		t.Errorf("saw %d hold events, want waiting then resumed", held)
	}
}

func TestAQuestionOnAUnaryCallIsAskedOnceConfirmedAndThatCallRetried(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}
	p := startFake(t, ctx, "unknown-host-key", span, answering(asker, io.Discard), fake.env()...)

	err := p.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{})
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
	if got := fake.recorded(t); got != fakeHostLine() {
		t.Errorf("known_hosts = %q, want %q", got, fakeHostLine())
	}
}

func TestAQuestionOnPreflightIsAskedOnceConfirmedAndThePreflightRetried(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}
	p := startFake(t, ctx, "unknown-host-key", span, answering(asker, io.Discard), fake.env()...)

	if _, err := Preflight(ctx, p, &contractv1.PreflightRequest{}); err != nil {
		t.Fatalf("Preflight() error = %v, want the retried preflight to answer", err)
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times (%v), want exactly one prompt", len(asker.asked), asker.asked)
	}
	if got := fake.drivenTimes(t); got != 2 {
		t.Errorf("the preflight ran %d times, want 2", got)
	}
}

func TestAQuestionDeclinedAtThePromptLeavesTheCallsErrorStandingAndConfirmsNothing(t *testing.T) {
	t.Parallel()

	ctx, span, seen := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: false}
	p := startFake(t, ctx, "unknown-host-key", span, answering(asker, io.Discard), fake.env()...)

	_, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
	if _, asked := questionIn(err); !asked {
		t.Errorf("Stream() error = %v, want the provider's question left standing", err)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := bodies(seen); !slices.Equal(got, []string{"waiting", "resumed answered"}) {
		t.Errorf("the span saw %v around the prompt, want the run held while it asked", got)
	}
}

func TestAProviderAnsweringThatItDoesNotSupportACallFailsWithProviderUnsupportedInItsOwnWords(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		call    func(context.Context, *Provider) error
		message string
	}{
		"unsupported-deploy": {func(ctx context.Context, p *Provider) error {
			_, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy)
			return err
		}, "simulated provider runs no serverless app"},
		"unsupported-deploy-result": {func(ctx context.Context, p *Provider) error {
			_, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy)
			return err
		}, "simulated provider runs no worker"},
		"unimplemented-logs": {func(ctx context.Context, p *Provider) error {
			_, err := readLogMessages(ctx, p)
			return err
		}, "simulated older provider"},
	}
	for mode, tc := range cases {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			ctx, span, _ := deploySpan(t)
			p := startFake(t, ctx, mode, span, Questions{})

			got := clierror.NewRunError(tc.call(ctx, p))
			if got.GetCode() != clierror.CodeProviderUnsupported {
				t.Errorf("run error code = %q, want %q", got.GetCode(), clierror.CodeProviderUnsupported)
			}
			if !strings.Contains(got.GetMessage(), tc.message) || strings.Contains(got.GetMessage(), "unimplemented") {
				t.Errorf("run error message = %q, want the provider's %q without the RPC's code", got.GetMessage(), tc.message)
			}
		})
	}
}

func answerFromTheJob(seen *recording, stdin *io.PipeWriter, confirmed bool) {
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			for _, event := range seen.received() {
				if input := event.GetWaiting().GetInput(); input != nil {
					_, _ = fmt.Fprintf(stdin, "{\"inputId\":%q,\"confirmed\":%t}\n", input.GetId(), confirmed)
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func TestInsideAJobAProvidersQuestionIsPutToOcelCloudAndItsAnswerTakenAsTheTerminalsWouldBe(t *testing.T) {
	t.Parallel()

	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			t.Parallel()

			ctx, span, seen := deploySpan(t)
			fake := newQuestionFake(t, "unknown-host-key")
			stdin, write := io.Pipe()
			answerFromTheJob(seen, write, confirmed)
			p := startFake(t, ctx, "unknown-host-key", span, Questions{Respondent: confirmation.Job{Answers: jobinput.New(stdin)}}, fake.env()...)

			_, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
			switch {
			case confirmed && err != nil:
				t.Fatalf("Stream() error = %v, want the call retried once Ocel Cloud confirmed", err)
			case !confirmed && clierror.NewRunError(err).GetCode() != clierror.CodeConfirmationRequired:
				t.Fatalf("Stream() error = %v, want %s once Ocel Cloud declined", err, clierror.CodeConfirmationRequired)
			}
			var asked *runnerv1.Confirmation
			for _, event := range seen.received() {
				if c := event.GetWaiting().GetInput().GetConfirmation(); c != nil {
					asked = c
				}
			}
			if !strings.Contains(asked.GetFinding(), fakeHostFingerprint) || asked.GetQuestion() == "" {
				t.Errorf("asked %v, want the provider's finding and prompt put to Ocel Cloud", asked)
			}
		})
	}
}
