package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/ocelhq/ocel/pkg/processenv"
)

const StalenessBound = 60 * time.Second

const FetchBudget = 3 * time.Second

const RereadFloor = time.Second

type Source interface {
	Fetch(ctx context.Context) (map[string]string, error)
}

type Expiring interface {
	RenewWithin() (time.Duration, bool)
}

// Sink must honor write deadlines, returning an error if it cannot accept data
// before the deadline, including on partial writes. Failed deliveries stay pending.
type Sink interface {
	io.Writer
	SetWriteDeadline(time.Time) error
}

type valuesMsg struct {
	Type       string            `json:"type"`
	Generation uint32            `json:"generation"`
	Values     map[string]string `json:"values"`
}

const valuesMsgType = "liveValues"

type Values struct {
	source   Source
	keys     []string
	bindings []Binding
	now      func() time.Time

	failed chan struct{}

	rereading  *semaphore.Weighted
	delivering *semaphore.Weighted

	mu          sync.Mutex
	failure     error
	sink        Sink
	sinkVersion uint64
	projection  *projection
	generation  uint32
	values      map[string]string
	shown       map[string]string
	fetchedAt   time.Time
	undelivered bool
	refreshing  bool
}

func New(source Source, keys []string, bindings []Binding, now func() time.Time) *Values {
	if now == nil {
		now = time.Now
	}
	return &Values{source: source, keys: keys, bindings: bindings, now: now, failed: make(chan struct{}), rereading: semaphore.NewWeighted(1), delivering: semaphore.NewWeighted(1)}
}

func (l *Values) read(ctx context.Context) (map[string]string, time.Time, error) {
	started := l.now()
	values, err := l.source.Fetch(ctx)
	if err != nil {
		return nil, started, err
	}
	if err := Conform(l.bindings, values); err != nil {
		return nil, started, err
	}
	return values, started, nil
}

func (l *Values) Env() []string {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	env := []string{processenv.LiveKeysEnvVar + "=" + strings.Join(l.keys, ",")}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.projection != nil {
		env = append(env, processenv.LiveDirEnvVar+"="+l.projection.root)
	}
	return env
}

func (l *Values) Value(key string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.values[key]
}

func (l *Values) Generation() uint32 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generation
}

func (l *Values) Keys() []string {
	if l == nil {
		return nil
	}
	return l.keys
}

func (l *Values) Bindings() []Binding {
	if l == nil {
		return nil
	}
	return l.bindings
}

func (l *Values) Prefetch(ctx context.Context) <-chan error {
	if l == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, FetchBudget)
		defer cancel()
		values, started, err := l.read(ctx)
		if err != nil {
			l.mu.Lock()
			l.failure = err
			l.mu.Unlock()
			close(l.failed)
			done <- err
			return
		}
		l.apply(ctx, values, started)
		done <- nil
	}()
	return done
}

func (l *Values) Abandoned() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.failed
}

func (l *Values) Failure() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

func (l *Values) Join(done <-chan error) error {
	if done == nil {
		return nil
	}
	return <-done
}

func (l *Values) Attach(sink Sink) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.sink = sink
	l.sinkVersion++
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), FetchBudget)
	defer cancel()
	l.deliver(ctx)
}

func (l *Values) Project(root string) error {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), FetchBudget)
	defer cancel()
	if err := l.delivering.Acquire(ctx, 1); err != nil {
		return err
	}
	defer l.delivering.Release(1)
	proj, err := newProjection(root, l.keys)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.projection = proj
	generation, values := l.generation, l.shown
	l.mu.Unlock()
	if generation > 0 {
		return proj.write(generation, values)
	}
	return nil
}

func (l *Values) Refresh(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.refreshing || l.generation == 0 || l.now().Sub(l.fetchedAt) < StalenessBound {
		l.mu.Unlock()
		return
	}
	l.refreshing = true
	l.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, FetchBudget)
		defer cancel()
		values, started, err := l.read(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ocel: live value refresh failed, serving the last resolved generation: %v\n", err)
			l.mu.Lock()
			l.refreshing = false
			l.mu.Unlock()
			return
		}
		l.mu.Lock()
		l.refreshing = false
		l.mu.Unlock()
		l.apply(ctx, values, started)
	}()
}

func (l *Values) Reread(ctx context.Context) {
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, FetchBudget)
	defer cancel()
	if err := l.rereading.Acquire(ctx, 1); err != nil {
		return
	}
	defer l.rereading.Release(1)

	l.mu.Lock()
	skip := l.generation == 0 || l.now().Sub(l.fetchedAt) < RereadFloor
	l.mu.Unlock()
	if skip {
		return
	}

	values, started, err := l.read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: live value reread failed, serving the last resolved generation: %v\n", err)
		return
	}
	l.apply(ctx, values, started)
}

func (l *Values) Freshen(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	stale := l.now().Sub(l.fetchedAt) >= StalenessBound
	l.mu.Unlock()
	if stale || l.isDueForRenewal() {
		l.Reread(ctx)
	}
}

func (l *Values) isDueForRenewal() bool {
	source, expiring := l.source.(Expiring)
	if !expiring {
		return false
	}
	within, expires := source.RenewWithin()
	if !expires {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generation > 0 && l.now().Sub(l.fetchedAt) >= within
}

func (l *Values) Keep(ctx context.Context) {
	if l == nil {
		return
	}
	ticker := time.NewTicker(StalenessBound)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Refresh(ctx)
		}
	}
}

func (l *Values) apply(ctx context.Context, values map[string]string, started time.Time) {
	if values == nil {
		values = map[string]string{}
	}
	l.mu.Lock()
	l.generation++
	l.values = values
	l.shown = shown(l.bindings, values)
	l.fetchedAt = started
	l.undelivered = true
	l.mu.Unlock()
	l.deliver(ctx)
}

func (l *Values) deliver(ctx context.Context) {
	if err := l.delivering.Acquire(ctx, 1); err != nil {
		return
	}
	defer l.delivering.Release(1)
	l.mu.Lock()
	if !l.undelivered {
		l.mu.Unlock()
		return
	}
	sink, version := l.sink, l.sinkVersion
	proj := l.projection
	msg := valuesMsg{Type: valuesMsgType, Generation: l.generation, Values: l.shown}
	l.mu.Unlock()
	if proj != nil {
		if err := proj.write(msg.Generation, msg.Values); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: could not project live values to %s: %v\n", proj.root, err)
		}
	}
	if sink == nil {
		return
	}
	line, err := json.Marshal(msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not encode live values: %v\n", err)
		return
	}
	deadline, _ := ctx.Deadline()
	if err := sink.SetWriteDeadline(deadline); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not bound live value delivery: %v\n", err)
		return
	}
	defer func() { _ = sink.SetWriteDeadline(time.Time{}) }()
	line = append(line, '\n')
	n, err := sink.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not push live values to node: %v\n", err)
		return
	}
	l.mu.Lock()
	if l.generation == msg.Generation && l.sinkVersion == version {
		l.undelivered = false
	}
	l.mu.Unlock()
}

func (l *Values) Missing() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var missing []string
	for _, key := range l.keys {
		if _, ok := l.values[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}
