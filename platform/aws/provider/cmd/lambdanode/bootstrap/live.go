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

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ocelhq/ocel/platform/aws/provider/awsconf"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

const liveStalenessBound = 60 * time.Second

const liveFetchBudget = 3 * time.Second

type liveFetcher interface {
	fetchLive(ctx context.Context) (map[string]string, error)
}

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

type liveValuesMsg struct {
	Type       string            `json:"type"`
	Generation uint32            `json:"generation"`
	Values     map[string]string `json:"values"`
}

const liveValuesMsgType = "liveValues"

type liveValues struct {
	fetcher liveFetcher
	keys    []string
	now     func() time.Time

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

const liveKeysEnvVar = "OCEL_LIVE_KEYS"

func (l *liveValues) declaredEnv() []string {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	return []string{liveKeysEnvVar + "=" + strings.Join(l.keys, ",")}
}

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

func (l *liveValues) prefetchFailed() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.failed
}

func (l *liveValues) prefetchError() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

func (l *liveValues) join(done <-chan error) error {
	if done == nil {
		return nil
	}
	return <-done
}

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

	cfg, err := awsconf.Runtime(ctx)
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

func manifestKeys(m live.Manifest) []string {
	keys := make([]string, 0, len(m.Keys))
	for _, k := range m.Keys {
		keys = append(keys, k.Key)
	}
	return keys
}

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
