package run_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
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
		out = append(out, ev.GetMessage())
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
			unit := deploy.Unit(app, progress.Deploying.Title(app))
			for range 50 {
				unit.Say("uploading " + app)
			}
			unit.End(nil)
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

func TestNoSinkReceivesASecretABindingInTheOutcomeCarries(t *testing.T) {
	const password, token = "hunter2-db-password", "sk-live-custom-token"
	custom, err := structpb.NewStruct(map[string]any{"apiToken": token})
	if err != nil {
		t.Fatal(err)
	}
	outcome := &progressv1.OperationResult{Success: true, Bindings: []*bindingsv1.Binding{
		{Name: "db", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host:     "db.internal",
			Username: "app",
			Password: password,
			Url:      "postgres://app:" + password + "@db.internal/app",
		}}},
		{Name: "payments", Properties: &bindingsv1.Binding_Custom{Custom: custom}},
	}}
	sink := &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(sink)
	dir := t.TempDir()
	_, run, err := bus.Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	logPath := runFile(t, dir, ".ndjson")

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: outcome}})
	run.End(&err)

	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var received []string
	for _, ev := range sink.received() {
		line, marshalErr := protojson.Marshal(ev)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		received = append(received, string(line))
	}
	for name, written := range map[string]string{"the run's log": string(logged), "an attached sink": strings.Join(received, "\n")} {
		for _, secret := range []string{password, token} {
			if strings.Contains(written, secret) {
				t.Errorf("%s holds %q, want no binding's secret: %s", name, secret, written)
			}
		}
		if !strings.Contains(written, "db.internal") || !strings.Contains(written, "apiToken") {
			t.Errorf("%s = %s, want each binding to keep where it points and the names of its properties", name, written)
		}
	}
	if outcome.GetBindings()[0].GetPostgres().GetPassword() != password || outcome.GetBindings()[1].GetCustom().GetFields()["apiToken"].GetStringValue() != token {
		t.Error("the provider's outcome lost its secrets: the deploy still reads them after the sinks")
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
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", progress.Deploying.Title("web"))
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
			t.Fatalf("%s sink's last event is a %T, want the result after every span ended", name, received[len(received)-1].GetBody())
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
		if ev.GetMessage() == "installing dependencies" {
			heard = append(heard, ev)
		}
	}
	if len(heard) != 1 {
		t.Fatalf("the sink heard the log %d times, want once", len(heard))
	}
	if ev := heard[0]; ev.GetLevel() != progressv1.Level_LEVEL_DEBUG || ev.GetPhase() != progressv1.Phase_PHASE_BUILD || ev.GetSubject() != "web" {
		t.Errorf("the log reads %s [%s] %q, want DEBUG [PHASE_BUILD] \"web\"", ev.GetLevel(), ev.GetPhase(), ev.GetSubject())
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if n := strings.Count(string(logged), "installing dependencies"); n != 1 {
		t.Errorf("the run's log holds the line %d times, want once:\n%s", n, logged)
	}
}
