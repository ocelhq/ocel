package live

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type expiringSource struct {
	*scriptedSource
	renewWithin time.Duration
	expires     bool
}

func (f expiringSource) RenewWithin() (time.Duration, bool) { return f.renewWithin, f.expires }

func TestFreshenRereadsStaleValuesAndCredentialsDueForRenewalBeforeReturning(t *testing.T) {
	t.Run("values an expiring source handed out past their renewal age are read again before Freshen returns", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		source := expiringSource{
			scriptedSource: resolves(map[string]string{"DB_TOKEN": "minted-first"}, map[string]string{"DB_TOKEN": "minted-again"}),
			renewWithin:    10 * time.Minute,
			expires:        true,
		}
		l := New(source, []string{"DB_TOKEN"}, nil, func() time.Time { return clock })
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(10 * time.Minute)
		l.Freshen(context.Background())

		if got := l.Value("DB_TOKEN"); got != "minted-again" {
			t.Errorf("Value() = %q as Freshen returns, want the token read again before the invocation runs on it", got)
		}
	})

	t.Run("values younger than the staleness bound cost no read", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		source := expiringSource{
			scriptedSource: resolves(map[string]string{"DB_TOKEN": "minted-first"}, map[string]string{"DB_TOKEN": "minted-again"}),
			renewWithin:    10 * time.Minute,
			expires:        true,
		}
		l := New(source, []string{"DB_TOKEN"}, nil, func() time.Time { return clock })
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound - time.Second)
		l.Freshen(context.Background())

		if source.startedFetches() != 1 {
			t.Errorf("the store was read %d times, want once: the token is still young", source.startedFetches())
		}
	})

	t.Run("an invocation waits for a stale secret to reach the app before it can finish and freeze the runtime", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		release := make(chan struct{})
		inner := resolves(map[string]string{"STORE_BASE": "first"}, map[string]string{"STORE_BASE": "second"})
		source := expiringSource{scriptedSource: inner, renewWithin: 10 * time.Minute}
		l := New(source, []string{"STORE_BASE"}, nil, func() time.Time { return clock })
		dir := t.TempDir()
		if err := l.Project(dir); err != nil {
			t.Fatal(err)
		}
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		inner.release = release
		clock = clock.Add(StalenessBound)
		done := make(chan struct{})
		go func() {
			l.Freshen(context.Background())
			close(done)
		}()
		eventually(t, "the stale secret fetch to start", func() bool { return inner.startedFetches() == 2 })
		select {
		case <-done:
			close(release)
			t.Fatal("Freshen returned before the store read completed, so Lambda can freeze it and serve the old secret indefinitely")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Freshen did not return after the store read completed")
		}
		if got := l.Value("STORE_BASE"); got != "second" {
			t.Errorf("Value() = %q as Freshen returns, want the current secret before the request reaches the app", got)
		}
		value, err := os.ReadFile(filepath.Join(dir, "STORE_BASE"))
		if err != nil {
			t.Fatal(err)
		}
		if string(value) != "second" {
			t.Errorf("the app's live file = %q, want the current secret before the request reaches the app", value)
		}
	})
}

func TestARereadWaitingForAnotherReadReturnsWhenItsContextIsCanceled(t *testing.T) {
	clock := &slowClock{now: time.Unix(1_700_000_000, 0)}
	source := resolves(map[string]string{"KEY": "old"}, map[string]string{"KEY": "new"})
	values := New(source, []string{"KEY"}, nil, clock.read)
	if err := values.Join(values.Prefetch(t.Context())); err != nil {
		t.Fatal(err)
	}
	clock.advance(StalenessBound)
	source.release = make(chan struct{})
	ownerDone := make(chan struct{})
	go func() { values.Reread(t.Context()); close(ownerDone) }()
	eventually(t, "the first reread to start", func() bool { return source.startedFetches() == 2 })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { values.Reread(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("the queued reread returned before cancellation or completion")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		close(source.release)
		<-ownerDone
		<-done
		t.Fatal("a canceled reread waited for the ongoing read")
	}
	close(source.release)
	<-ownerDone
	if source.startedFetches() != 2 {
		t.Fatal("a canceled waiter read the store")
	}
}

func TestARereadIncludesItsQueueWaitInTheFetchBudget(t *testing.T) {
	clock := &slowClock{now: time.Unix(1_700_000_000, 0)}
	source := resolves(map[string]string{"KEY": "old"})
	values := New(source, []string{"KEY"}, nil, clock.read)
	if err := values.Join(values.Prefetch(t.Context())); err != nil {
		t.Fatal(err)
	}
	clock.advance(StalenessBound)
	source.release = make(chan struct{})
	defer close(source.release)
	ownerDone := make(chan struct{})
	go func() { values.Reread(t.Context()); close(ownerDone) }()
	eventually(t, "the first reread to start", func() bool { return source.startedFetches() == 2 })
	done := make(chan struct{})
	go func() { values.Reread(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(FetchBudget + 500*time.Millisecond):
		t.Fatal("the queued reread received a new fetch budget after waiting")
	}
	<-ownerDone
}

type notifyingSink struct {
	net.Conn
	writing chan struct{}
}

func (s *notifyingSink) Write(p []byte) (int, error) {
	if s.writing != nil {
		close(s.writing)
		s.writing = nil
	}
	return s.Conn.Write(p)
}

func TestFreshenBoundsDeliveryAndKeepsValuesReadableWhileTheSinkIsBlocked(t *testing.T) {
	clock := &slowClock{now: time.Unix(1_700_000_000, 0)}
	source := resolves(map[string]string{"KEY": "old"}, map[string]string{"KEY": "new"})
	values := New(source, []string{"KEY"}, nil, clock.read)
	if err := values.Join(values.Prefetch(t.Context())); err != nil {
		t.Fatal(err)
	}
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	initial := make(chan struct{})
	go func() { _, _ = bufio.NewReader(reader).ReadBytes('\n'); close(initial) }()
	out := &notifyingSink{Conn: writer}
	values.Attach(out)
	<-initial
	out.writing = make(chan struct{})
	writing := out.writing
	clock.advance(StalenessBound)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { values.Freshen(ctx); close(done) }()
	select {
	case <-writing:
	case <-time.After(time.Second):
		t.Fatal("the fresh generation never reached the sink")
	}
	read := make(chan string, 1)
	go func() { read <- values.Value("KEY") }()
	select {
	case value := <-read:
		if value != "new" {
			t.Errorf("Value = %q, want the fetched value", value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("a blocked sink holds the values mutex")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		writer.Close()
		<-done
		t.Fatal("delivery outlived the refresh context")
	}
	retry := &sink{}
	values.Attach(retry)
	messages := retry.messages(t)
	if len(messages) != 1 || messages[0].Generation != 2 || messages[0].Values["KEY"] != "new" {
		t.Fatalf("retry delivered %+v, want the pending second generation", messages)
	}
}
