package run_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type recording struct {
	mu     sync.Mutex
	events []*streamv1.RunEvent
	closed bool
}

func (r *recording) Receive(ev *streamv1.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recording) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *recording) received() []*streamv1.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*streamv1.RunEvent(nil), r.events...)
}

func (r *recording) messages() []string {
	var out []string
	for _, ev := range r.received() {
		out = append(out, ev.GetOperation().GetMessage())
	}
	return out
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)} }

func (c *clock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func runFile(t *testing.T, projectDir, extension string) string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(projectDir, statedir.Name, "runs", "*"+extension))
	if err != nil || len(found) != 1 {
		t.Fatalf("run files = %v, %v, want the run's one %s file", found, err, extension)
	}
	return found[0]
}

func begin(t *testing.T, sinks ...run.Sink) (*run.Run, *clock) {
	t.Helper()
	return beginIn(t, context.Background(), sinks...)
}

func beginIn(t *testing.T, ctx context.Context, sinks ...run.Sink) (*run.Run, *clock) {
	t.Helper()
	c := newClock()
	bus := run.NewBus(c.read)
	for _, s := range sinks {
		bus.Attach(s)
	}
	_, run, err := bus.Begin(ctx, "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return run, c
}

func TestEveryAttachedSinkSeesEveryEventInOrder(t *testing.T) {
	first, second := &recording{}, &recording{}
	run, _ := begin(t, first, second)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	check.Say("Checking credentials")
	check.Warn("the edge plan is unknown")
	check.Say("Credentials are valid")

	want := []string{"", "Checking credentials", "the edge plan is unknown", "Credentials are valid"}
	for name, sink := range map[string]*recording{"first": first, "second": second} {
		got := sink.messages()
		if len(got) != len(want) {
			t.Fatalf("%s sink got %q, want %q", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s sink got %q, want %q", name, got, want)
			}
		}
	}
}

func TestSinksSeeEventsFromConcurrentSpansInTheSameOrder(t *testing.T) {
	first, second := &recording{}, &recording{}
	run, _ := begin(t, first, second)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)

	var wg sync.WaitGroup
	for _, app := range []string{"web", "api", "worker"} {
		wg.Go(func() {
			child := deploy.Child(app, progress.Deploying.Title(app))
			for range 50 {
				child.Say("uploading " + app)
			}
			child.End(nil)
		})
	}
	wg.Wait()

	a, b := first.received(), second.received()
	if len(a) != 1+3*52 || len(a) != len(b) {
		t.Fatalf("sinks got %d and %d events, want %d each", len(a), len(b), 1+3*52)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("event %d differs between sinks", i)
		}
	}
}

func TestClosingTheBusClosesEverySink(t *testing.T) {
	first, second := &recording{}, &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(first)
	bus.Attach(second)

	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}

	if !first.closed || !second.closed {
		t.Fatalf("closed = %v and %v, want both", first.closed, second.closed)
	}
}

func TestASecondInterruptEndsTheOpenRunAsInterruptedOnEverySinkAndClosesThem(t *testing.T) {
	first, second := &recording{}, &recording{}
	bus := run.NewBus(newClock().read)
	bus.Attach(first)
	bus.Attach(second)
	ctx, cancel := context.WithCancel(context.Background())
	_, run, err := bus.Begin(ctx, "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Child("web", progress.Deploying.Title("web"))
	cancel()

	bus.Interrupt()
	run.End(&err)

	for name, sink := range map[string]*recording{"first": first, "second": second} {
		received := sink.received()
		var results []*streamv1.RunSummary
		for _, ev := range received {
			if ev.GetSummary() != nil {
				results = append(results, ev.GetSummary())
			}
		}
		if len(results) != 1 || !results[0].GetInterrupted() || results[0].GetHeadline() != "Deploy cancelled" {
			t.Fatalf("%s sink got %d results (first interrupted: %t), want one interrupted result", name, len(results), len(results) > 0 && results[0].GetInterrupted())
		}
		if received[len(received)-1].GetSummary() == nil {
			t.Fatalf("%s sink's last event is a %T, want the result after every span ended", name, received[len(received)-1].GetCli())
		}
		if !sink.closed {
			t.Fatalf("%s sink was not closed, want the interrupt to flush it", name)
		}
	}
}

func TestATraceSpansDebugDetailReachesEverySinkAsDetailOfItsSubjectAndTheRunsLogOnce(t *testing.T) {
	sink := &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(sink)
	dir := t.TempDir()
	_, run, err := bus.Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	logPath := runFile(t, dir, ".ndjson")

	traced := run.Phase(progressv1.Phase_PHASE_BUILD).Trace("web", "build")
	traced.Debug("installing dependencies")
	traced.End(nil)
	run.End(&err)

	var heard []*streamv1.RunEvent
	for _, ev := range sink.received() {
		if ev.GetOperation().GetMessage() == "installing dependencies" {
			heard = append(heard, ev)
		}
	}
	if len(heard) != 1 {
		t.Fatalf("the sink heard the log %d times, want once", len(heard))
	}
	if ev := heard[0]; ev.GetOperation().GetLevel() != progressv1.Level_LEVEL_DEBUG || ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD || ev.GetOperation().GetSubject() != "web" {
		t.Errorf("the log reads %s [%s] %q, want DEBUG [PHASE_BUILD] \"web\"", ev.GetOperation().GetLevel(), ev.GetOperation().GetPhase(), ev.GetOperation().GetSubject())
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if n := strings.Count(string(logged), "installing dependencies"); n != 1 {
		t.Errorf("the run's log holds the line %d times, want once:\n%s", n, logged)
	}
}

func TestNoFieldReachableFromARunEventCanCarryASecret(t *testing.T) {
	found := secretBearingFields((&streamv1.RunEvent{}).ProtoReflect().Descriptor())
	if len(found) > 0 {
		t.Fatalf("run events reach secret-bearing fields %v; the bus writes every event to the terminal, the run log and the trace unredacted", found)
	}
}

func TestTheSecretFieldWalkFindsARedactedFieldInAReachableMessage(t *testing.T) {
	found := secretBearingFields((&bindingsv1.Binding{}).ProtoReflect().Descriptor())
	if !slices.Contains(found, "common.bindings.v1.PostgresProperties.password") {
		t.Fatalf("secret-bearing fields of a binding = %v, want the postgres password among them", found)
	}
}

func secretBearingFields(root protoreflect.MessageDescriptor) []string {
	var found []string
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		switch md.FullName() {
		case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue", "google.protobuf.Any":
			found = append(found, string(md.FullName()))
			return
		}
		fields := md.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			if opts, ok := fd.Options().(*descriptorpb.FieldOptions); ok && opts.GetDebugRedact() {
				found = append(found, string(fd.FullName()))
			}
			if fd.IsMap() {
				fd = fd.MapValue()
			}
			if fd.Message() != nil {
				walk(fd.Message())
			}
		}
	}
	walk(root)
	return found
}

func TestAPreambleSaysAndAsksBeforeAnyRunBeginsAndSummarisesNothing(t *testing.T) {
	bus := run.NewBus(time.Now)
	seen := &recording{}
	bus.Attach(seen)

	preamble := bus.Preamble()
	preamble.Say("No ocel.json here.")
	if _, err := preamble.Confirm(func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}

	events := seen.received()
	if len(events) != 3 {
		t.Fatalf("events = %v, want the message, the hold and the resume", events)
	}
	said := events[0].GetOperation()
	if said.GetMessage() != "No ocel.json here." || len(said.GetSpanId()) != 0 || said.GetPhase() != progressv1.Phase_PHASE_UNSPECIFIED {
		t.Errorf("said %v, want the message with no scope", said)
	}
	if events[1].GetWaiting() == nil || events[2].GetResumed() == nil {
		t.Errorf("events = %v, want the question held and resumed", events)
	}
	for _, ev := range events {
		if ev.GetSummary() != nil {
			t.Errorf("a preamble summarised %v, want it to leave the summary to the run that follows", ev.GetSummary())
		}
	}
}
