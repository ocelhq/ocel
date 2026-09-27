package events_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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

func begin(t *testing.T, sinks ...events.Sink) (*events.Run, *clock) {
	t.Helper()
	return beginIn(t, context.Background(), sinks...)
}

func beginIn(t *testing.T, ctx context.Context, sinks ...events.Sink) (*events.Run, *clock) {
	t.Helper()
	c := newClock()
	bus := events.NewBus(c.read)
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

func TestSinksSeeEventsFromConcurrentScopesInTheSameOrder(t *testing.T) {
	first, second := &recording{}, &recording{}
	run, _ := begin(t, first, second)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)

	var wg sync.WaitGroup
	for _, app := range []string{"web", "api", "worker"} {
		wg.Go(func() {
			unit := deploy.Unit(app, "Deploying "+app)
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
	bus := events.NewBus(time.Now)
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
	outcome := &progressv1.ResultEvent{Success: true, Bindings: []*bindingsv1.Binding{
		{Name: "db", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host:     "db.internal",
			Username: "app",
			Password: password,
			Url:      "postgres://app:" + password + "@db.internal/app",
		}}},
		{Name: "payments", Properties: &bindingsv1.Binding_Custom{Custom: custom}},
	}}
	sink := &recording{}
	bus := events.NewBus(time.Now)
	bus.Attach(sink)
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := runtrace.FromContext(ctx).LogPath()

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
