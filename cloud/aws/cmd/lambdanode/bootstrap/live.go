package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ocelhq/ocel/cloud/aws/vars"
	"github.com/ocelhq/ocel/cloud/aws/vars/live"
)

// liveStalenessBound is how long a resolved live value may go on being served
// before a refresh is started for it. It is one bound for every variable in the
// project, because rotation latency is a project-wide operational property
// rather than a per-variable one.
//
// It is not user-configurable yet. Making it so means a settings surface on the
// project config, which does not exist, and a wire field to carry it; until
// then this constant is the single place the bound is stated, and changing it
// is a redeploy. That is a deliberate difference from the values themselves,
// which rotate without one.
//
// The clock is read when an invocation arrives, not on a timer: Lambda freezes
// the sandbox between invocations, so a goroutine parked on a ticker does not
// reliably wake. An idle sandbox therefore holds a value past the bound and
// refreshes it as soon as it is asked to do any work at all, which is the
// strongest guarantee a frozen process can make.
const liveStalenessBound = 60 * time.Second

// liveFetchBudget bounds the prefetch. It sits inside startupBudget rather than
// beside it: the fetch runs concurrently with node's boot, so what it spends is
// only visible if node comes up first, and a fetch still running when node is
// ready is what an application blocks on. Failing at three seconds leaves room
// to report a diagnosable init error instead of being killed with nothing to
// say.
const liveFetchBudget = 3 * time.Second

// liveFetcher resolves this function's live-class values. It is an interface so
// the cache, the push and the refresh clock can be tested without an AWS client
// anywhere near them.
type liveFetcher interface {
	fetchLive(ctx context.Context) (map[string]string, error)
}

// storeFetcher reads the coordinates the deploy pinned, through the store's own
// read. Going through vars.Store rather than a hand-rolled decrypt is what
// makes the KMS encryption context this function presents the one the value was
// sealed under: the context is computed by the single function that defines it.
type storeFetcher struct {
	store *vars.Store
	slug  string
	cells []vars.Coordinate
}

func (f storeFetcher) fetchLive(ctx context.Context) (map[string]string, error) {
	values, err := f.store.Reveal(ctx, f.slug, f.cells)
	if err != nil {
		return nil, err
	}
	return resolved(values), nil
}

// resolved is the precedence between the two cells a key can answer from: the
// override this environment holds wins, and a key it holds none for falls
// through to the class-wide value. It is two passes rather than one because the
// store answers in key order, which says nothing about which of a key's cells
// came back first.
//
// A production function only ever reads one cell per key, so both passes see
// the same set and the second does nothing.
func resolved(values []vars.Value) map[string]string {
	out := make(map[string]string, len(values))
	for _, v := range values {
		if v.Coordinate.Environment == "" {
			out[v.Coordinate.Key] = v.Plaintext
		}
	}
	for _, v := range values {
		if v.Coordinate.Environment != "" {
			out[v.Coordinate.Key] = v.Plaintext
		}
	}
	return out
}

// liveValuesMsg is the control-socket message the membrane pushes to node. It
// is one-way and fire-and-forget: node holds the values in memory and answers
// application reads out of that map, so the read stays a plain synchronous
// property access whatever class the key is.
//
// generation is monotonic from 1. Node ignores a message whose generation is
// not greater than the one it applied, so a refresh that lands out of order can
// never resurrect an older value.
type liveValuesMsg struct {
	Type       string            `json:"type"`
	Generation uint32            `json:"generation"`
	Values     map[string]string `json:"values"`
}

const liveValuesMsgType = "liveValues"

// liveValues is the membrane-side cache of this function's live values, shared
// by every invocation the execution environment serves. A nil *liveValues is a
// function that declares none: every method is a no-op on it, which is what
// keeps a store outage confined to the functions that actually read the store.
type liveValues struct {
	fetcher liveFetcher
	keys    []string
	now     func() time.Time

	// failed is closed when the prefetch fails, and failure is what it said.
	// Node waits for a push before it imports the application, so a prefetch
	// that failed is also the reason node will never announce itself: the wait
	// for it has to end on this, or the store's error is buried under a
	// startup timeout that names nothing.
	failed chan struct{}

	mu          sync.Mutex
	failure     error
	sink        io.Writer
	generation  uint32
	values      map[string]string
	fetchedAt   time.Time
	undelivered bool
	refreshing  bool
}

func newLiveValues(fetcher liveFetcher, keys []string, now func() time.Time) *liveValues {
	if now == nil {
		now = time.Now
	}
	return &liveValues{fetcher: fetcher, keys: keys, now: now, failed: make(chan struct{})}
}

// liveKeysEnvVar names the live-class keys a function declares. It is the one
// thing node cannot work out for itself: the declarations live in the
// application, which node has not imported yet, so the membrane has to say at
// spawn whether a push is coming.
const liveKeysEnvVar = "OCEL_LIVE_KEYS"

// declaredEnv is that statement, in the form the child's environment takes. It
// carries bare key names and nothing else — no folder, no coordinate, no value
// — so the runtime stays as folder-blind for this class as for every other.
//
// A function that declares none gets no entry at all. The variable's presence
// is the whole of "wait for a push"; naming it on a function nothing will be
// pushed to would leave that function waiting until the startup budget killed
// it.
func (l *liveValues) declaredEnv() []string {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	return []string{liveKeysEnvVar + "=" + strings.Join(l.keys, ",")}
}

// start kicks the prefetch off and returns without waiting for it, so the fetch
// and decrypt run beside the child's exec and boot rather than in front of
// them. The returned channel carries the prefetch's outcome for a caller that
// joins it later; a nil cache returns nil, meaning there is nothing to join.
func (l *liveValues) start(ctx context.Context) <-chan error {
	if l == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, liveFetchBudget)
		defer cancel()
		values, err := l.fetcher.fetchLive(ctx)
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

// prefetchFailed reports the prefetch giving up, for a caller waiting on
// something that cannot happen without it. A nil cache returns a nil channel,
// which blocks forever: a function that declares no live value has no prefetch
// to fail, and nothing about it should ever end early on this.
func (l *liveValues) prefetchFailed() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.failed
}

// prefetchError is what the prefetch said, or nil while it is still in flight
// or once it has succeeded.
func (l *liveValues) prefetchError() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

// join waits for the prefetch to settle. It is called after the child is
// started and before the runtime loop begins, which is the last moment an init
// failure can still be reported as one: a value the application cannot be given
// must stop the function coming up, not surface as a variable that reads as
// unset.
func (l *liveValues) join(done <-chan error) error {
	if done == nil {
		return nil
	}
	return <-done
}

// attach installs the control connection and delivers any generation produced
// before node connected. The prefetch usually wins that race, so without this
// the first generation would sit in the membrane while the application blocked
// waiting for it.
func (l *liveValues) attach(sink io.Writer) {
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

// refreshIfStale starts a refresh when the current generation has been served
// past the bound, and returns immediately either way. Nothing waits on the
// result: the last generation goes on being served while the new one is
// fetched, so no request ever blocks on a refresh once a value has resolved.
//
// A refresh already in flight is not started again. The sandbox may be frozen
// mid-fetch and thawed invocations later, so "in flight" can span a long gap in
// wall-clock time, and stacking fetches on every invocation would turn one slow
// call into a pile of them.
func (l *liveValues) refreshIfStale(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.refreshing || l.generation == 0 || l.now().Sub(l.fetchedAt) < liveStalenessBound {
		l.mu.Unlock()
		return
	}
	l.refreshing = true
	l.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, liveFetchBudget)
		defer cancel()
		values, err := l.fetcher.fetchLive(ctx)
		if err != nil {
			// A failed refresh pushes nothing: node keeps serving the last
			// generation rather than being handed an empty one, which at the
			// point of use would read as a variable that was never required.
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

// apply records a newly resolved generation and pushes it. A fetch that found
// nothing is still a generation, and it is held as an empty map rather than a
// nil one: nil marshals to `null`, which node rejects as a malformed push and
// goes on waiting for, so a store holding none of this function's keys would
// wedge every cold start.
func (l *liveValues) apply(values map[string]string) {
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
}

// deliver writes the current generation to node, or leaves it marked
// undelivered when node has not connected yet. Only the newest generation is
// ever held: node ignores anything not greater than what it applied, so an
// older one has no reader.
//
// Callers hold l.mu.
func (l *liveValues) deliver() {
	if l.sink == nil {
		return
	}
	line, err := json.Marshal(liveValuesMsg{Type: liveValuesMsgType, Generation: l.generation, Values: l.values})
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

// resolveLiveValues builds the cache from the manifest the deploy packaged, and
// only when there is one. An app that declares no live value gets no manifest
// file, so it constructs no client, resolves no credentials and makes no call —
// the same shape the cache-store fetch uses for an unconfigured parameter, and
// what confines a store outage to the functions that depend on the store.
func resolveLiveValues(ctx context.Context) (*liveValues, error) {
	raw, err := os.ReadFile(filepath.Join(taskRoot(), live.FilePath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", live.FilePath, err)
	}
	manifest, err := live.Parse(raw)
	if err != nil {
		return nil, err
	}
	if len(manifest.Keys) == 0 {
		return nil, nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return newLiveValues(storeFetcher{
		store: &vars.Store{
			Dynamo: dynamodb.NewFromConfig(cfg),
			KMS:    kms.NewFromConfig(cfg),
			Table:  manifest.Table,
			KeyARN: manifest.KeyARN,
			Class:  manifest.Class,
		},
		slug:  manifest.Slug,
		cells: manifestCells(manifest),
	}, manifestKeys(manifest), nil), nil
}

// manifestKeys is the bare names the pinned coordinates resolve to, which is
// all node is told about them.
func manifestKeys(m live.Manifest) []string {
	keys := make([]string, 0, len(m.Keys))
	for _, k := range m.Keys {
		keys = append(keys, k.Key)
	}
	return keys
}

// manifestCells renders the pinned addresses as store coordinates: the
// class-wide cell for every key, and — only where the deploy pinned a named
// environment — that environment's override beside it. Both are read in the one
// query the store's reveal already costs, so looking for an override adds no
// round trip and finding none costs nothing.
//
// The class-wide cell's environment component is left empty rather than
// spelled: the sentinel is the store's own rendering of it, and a coordinate
// that names it literally is refused.
func manifestCells(m live.Manifest) []vars.Coordinate {
	cells := make([]vars.Coordinate, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, vars.Coordinate{Slug: m.Slug, Folder: k.Folder, Key: k.Key})
		if m.Environment != "" {
			cells = append(cells, vars.Coordinate{Slug: m.Slug, Folder: k.Folder, Key: k.Key, Environment: m.Environment})
		}
	}
	return cells
}
