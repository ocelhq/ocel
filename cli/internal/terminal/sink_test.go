package terminal

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func resultEvent(result *streamv1.RunSummary) *streamv1.RunEvent {
	return &streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO}, Cli: &streamv1.RunEvent_Summary{Summary: result}}
}

func missingStripeKey() *streamv1.MissingVariables {
	return &streamv1.MissingVariables{
		Cells:  []*streamv1.MissingVariable{{Key: "STRIPE_API_KEY", Reason: "no value"}},
		Remedy: "ocel env ui",
	}
}

func onABus(t *testing.T, ctx context.Context, now func() time.Time, sink run.Sink) (*run.Bus, *run.Run) {
	t.Helper()
	bus := run.NewBus(now)
	bus.Attach(sink)
	_, run, err := bus.Begin(ctx, "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return bus, run
}

func TestProviderProcessOutputShowsOnlyWhenVerbose(t *testing.T) {
	t.Parallel()
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%t", verbose), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			sink := newTranscript(&out, Presentation{Verbose: verbose}, nil)

			const marker = "raw subprocess output"
			sink.Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_DEBUG, Phase: progressv1.Phase_PHASE_CHECK, Subject: "fake", Message: marker, Body: &progressv1.OperationEvent_Output{
				Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDOUT},
			}}})

			if shown := strings.Contains(out.String(), marker+"\n"); shown != verbose {
				t.Errorf("stdout = %q, shows the provider process output = %v, want %v: it is debug output", out.String(), shown, verbose)
			}
		})
	}
}

func TestASpanRepaintingOneLineShowsOnlyTheDraftItLeft(t *testing.T) {
	t.Parallel()
	run, out, _ := groupedRun(t, Presentation{})

	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	tracked := build.Child("shop", progress.Building.Title("project"))
	w := tracked.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	for i := 1; i <= 500; i++ {
		if _, err := fmt.Fprintf(w, "\rProgress: resolved %d", i); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	tracked.End(nil)
	build.End(nil)

	got := out.String()
	if strings.Contains(got, "Progress: resolved 1\n") || strings.Count(got, "Progress: resolved") != 1 {
		t.Errorf("stdout = %q, want the repainted line shown once, as the draft the build left behind", got)
	}
	if !strings.Contains(got, verbatimIndent+"Progress: resolved 500\n") {
		t.Errorf("stdout = %q, want the last draft inside the build block", got)
	}
}

func TestAMessageWithNoScopeAlwaysReachesTheTerminalRegardlessOfVerbosity(t *testing.T) {
	t.Parallel()
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%t", verbose), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			sink := newTranscript(&out, Presentation{Verbose: verbose}, nil)

			sink.Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO, Message: "no functions to deploy; deploying infrastructure only"}})

			if want := "INFO  no functions to deploy; deploying infrastructure only\n"; out.String() != want {
				t.Errorf("stdout = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestAnEndedScopeWithoutAUsableEndRunsUntilItReachedTheBus(t *testing.T) {
	t.Parallel()

	stage := []byte{7, 0, 0, 0, 0, 0, 0, 0}
	now := time.Now().UTC()
	start := now.Add(-2 * time.Minute)

	for _, tc := range []struct {
		name string
		end  *timestamppb.Timestamp
	}{
		{"missing end", nil},
		{"end before start", timestamppb.New(start.Add(-time.Minute))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			_, run := onABus(t, context.Background(), func() time.Time { return now }, newTranscript(&out, Presentation{}, nil))

			deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
			deploy.Forward(&progressv1.OperationEvent{Phase: progressv1.Phase_PHASE_DEPLOY, Subject: "web", SpanId: stage, Message: "deploying web", Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{}}})
			deploy.Forward(&progressv1.OperationEvent{Time: tc.end, Phase: progressv1.Phase_PHASE_DEPLOY, Subject: "web", SpanId: stage, Body: &progressv1.OperationEvent_Ended{
				Ended: &progressv1.Ended{Status: progressv1.SpanStatus_SPAN_STATUS_OK, StartTimeUnixNano: start.UnixNano(), Title: "deploying web"},
			}})

			if want := "INFO  [deploy] ✓ web: deploying web in 2m00s\n"; out.String() != want {
				t.Errorf("stdout = %q, want %q: the span ran for the 2m until its end reached the bus", out.String(), want)
			}
		})
	}
}

func TestASpansOutputKeepsItsRightHandWhitespace(t *testing.T) {
	t.Parallel()
	run, out, _ := groupedRun(t, Presentation{})

	const padded = "Route (app)                     Size     First Load JS   "
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Child("web", progress.Building.Title("web"))
	output(t, web, padded)
	web.End(nil)

	if got := out.String(); !strings.Contains(got, "\n"+verbatimIndent+padded+"\n") {
		t.Errorf("stdout = %q, want the line as the stream sent it", got)
	}
}
