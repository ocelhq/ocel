package events_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
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
