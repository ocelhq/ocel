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

	"github.com/ocelhq/ocel/pkg/constants"
)

const StalenessBound = 60 * time.Second

const FetchBudget = 3 * time.Second

const RereadFloor = time.Second

type Fetcher interface {
	FetchLive(ctx context.Context) (map[string]string, error)
}

type valuesMsg struct {
	Type       string            `json:"type"`
	Generation uint32            `json:"generation"`
	Values     map[string]string `json:"values"`
}

const valuesMsgType = "Values"

type Values struct {
	fetcher  Fetcher
	keys     []string
	bindings []Binding
	now      func() time.Time

	failed chan struct{}

	rereading sync.Mutex

	mu          sync.Mutex
	failure     error
	sink        io.Writer
	projection  *projection
	generation  uint32
	values      map[string]string
	fetchedAt   time.Time
	undelivered bool
	refreshing  bool
}

func New(fetcher Fetcher, keys []string, bindings []Binding, now func() time.Time) *Values {
	if now == nil {
		now = time.Now
	}
	return &Values{fetcher: fetcher, keys: keys, bindings: bindings, now: now, failed: make(chan struct{})}
}

func (l *Values) read(ctx context.Context) (map[string]string, error) {
	values, err := l.fetcher.FetchLive(ctx)
	if err != nil {
		return nil, err
	}
	if err := Conform(l.bindings, values); err != nil {
		return nil, err
	}
	return values, nil
}

func (l *Values) Env() []string {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	env := []string{constants.LiveKeysEnvName + "=" + strings.Join(l.keys, ",")}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.projection != nil {
		env = append(env, constants.LiveDirEnvName+"="+l.projection.root)
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
		values, err := l.read(ctx)
		if err != nil {
			l.mu.Lock()
			l.failure = err
			l.mu.Unlock()
			close(l.failed)
			done <- err
			return
		}
		l.apply(values)
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

func (l *Values) Attach(sink io.Writer) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sink = sink
	if l.undelivered {
		l.deliver()
	}
}

func (l *Values) Project(root string) error {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	held, err := newProjection(root, l.keys)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.projection = held
	if l.generation > 0 {
		return held.write(l.generation, l.values)
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
		values, err := l.read(ctx)
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
		l.apply(values)
	}()
}

func (l *Values) Reread(ctx context.Context) {
	if l == nil {
		return
	}
	l.rereading.Lock()
	defer l.rereading.Unlock()

	l.mu.Lock()
	skip := l.generation == 0 || l.now().Sub(l.fetchedAt) < RereadFloor
	l.mu.Unlock()
	if skip {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, FetchBudget)
	defer cancel()
	values, err := l.read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: live value reread failed, serving the last resolved generation: %v\n", err)
		return
	}
	l.apply(values)
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

func (l *Values) apply(values map[string]string) {
	if values == nil {
		values = map[string]string{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	l.values = values
	l.fetchedAt = l.now()
	l.undelivered = true
	l.deliver()
	if l.projection != nil {
		if err := l.projection.write(l.generation, l.values); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: could not project live values to %s: %v\n", l.projection.root, err)
		}
	}
}

func (l *Values) deliver() {
	if l.sink == nil {
		return
	}
	line, err := json.Marshal(valuesMsg{Type: valuesMsgType, Generation: l.generation, Values: l.values})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not encode live values: %v\n", err)
		return
	}
	if _, err := l.sink.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not push live values to node: %v\n", err)
		return
	}
	l.undelivered = false
}

func (l *Values) Missing() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var missing []string
	for _, key := range l.keys {
		if _, held := l.values[key]; !held {
			missing = append(missing, key)
		}
	}
	return missing
}
