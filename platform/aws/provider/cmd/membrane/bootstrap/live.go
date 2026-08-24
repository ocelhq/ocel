package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const liveStalenessBound = 60 * time.Second

const liveFetchBudget = 3 * time.Second

type liveFetcher interface {
	fetchLive(ctx context.Context) (map[string]string, error)
}

type storeFetcher struct {
	reader values.Reader
	cells  []values.Cell
	links  []live.Link

	mu       sync.Mutex
	reported map[string]int64
}

func (f *storeFetcher) fetchLive(ctx context.Context) (map[string]string, error) {
	resolved, err := f.reader.Values(ctx, f.cells)
	if err != nil {
		return nil, err
	}
	records, err := f.reader.Links(ctx, linkNames(f.links))
	if err != nil {
		return nil, err
	}
	for _, lag := range f.unreportedGrantLag(records) {
		fmt.Fprintln(os.Stderr, "ocel: "+lag)
	}
	return merged(resolved, f.links, records), nil
}

func (f *storeFetcher) unreportedGrantLag(records []values.Published) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reported == nil {
		f.reported = map[string]int64{}
	}

	var out []string
	for _, lag := range grantLag(f.links, records) {
		if f.reported[lag.Name] == lag.Version {
			continue
		}
		f.reported[lag.Name] = lag.Version
		out = append(out, lag.Message)
	}
	return out
}

type lagged struct {
	Name    string
	Version int64
	Message string
}

func grantLag(links []live.Link, records []values.Published) []lagged {
	var out []lagged
	for i, record := range records {
		granted := links[i].Granted
		if granted == 0 || record.Version <= granted {
			continue
		}
		out = append(out, lagged{Name: record.Name, Version: record.Version, Message: fmt.Sprintf(
			"link %s has been published %s since this deployment's IAM grants were rendered, from version %d. "+
				"Its values are live and current; its permissions are not, and ocel widens no permission on its own — deploy again to move them to version %d",
			record.Name, republished(record.Version-granted), granted, record.Version,
		)})
	}
	return out
}

func republished(n int64) string {
	if n == 1 {
		return "once more"
	}
	return fmt.Sprintf("%d more times", n)
}

func linkNames(links []live.Link) []string {
	names := make([]string, 0, len(links))
	for _, l := range links {
		names = append(names, l.Name)
	}
	return names
}

func merged(resolved map[string]string, links []live.Link, records []values.Published) map[string]string {
	out := make(map[string]string, len(resolved)+len(records))
	maps.Copy(out, resolved)
	for i, record := range records {
		out[links[i].Key] = string(record.Value)
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
	links   []live.Link
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

func newLiveValues(fetcher liveFetcher, keys []string, links []live.Link, now func() time.Time) *liveValues {
	if now == nil {
		now = time.Now
	}
	return &liveValues{fetcher: fetcher, keys: keys, links: links, now: now, failed: make(chan struct{})}
}

func (l *liveValues) resolve(ctx context.Context) (map[string]string, error) {
	values, err := l.fetcher.fetchLive(ctx)
	if err != nil {
		return nil, err
	}
	if err := live.Conform(l.links, values); err != nil {
		return nil, err
	}
	return values, nil
}

const liveKeysEnvVar = "OCEL_LIVE_KEYS"

func (l *liveValues) declaredEnv() []string {
	if l == nil || len(l.keys) == 0 {
		return nil
	}
	return []string{liveKeysEnvVar + "=" + strings.Join(l.keys, ",")}
}

func (l *liveValues) declaredLinks() []live.Link {
	if l == nil {
		return nil
	}
	return l.links
}

func (l *liveValues) start(ctx context.Context) <-chan error {
	if l == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, liveFetchBudget)
		defer cancel()
		values, err := l.resolve(ctx)
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
		values, err := l.resolve(ctx)
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
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", live.FilePath, err)
	}
	manifest, err := live.Parse(raw)
	if err != nil {
		return nil, err
	}
	if len(manifest.Keys) == 0 && len(manifest.Links) == 0 {
		return nil, nil
	}

	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return newLiveValues(&storeFetcher{
		reader: values.Reader{
			Records:     awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: awsports.Table(manifest.Table)},
			Sealer:      awsports.Sealer{KMS: kms.NewFromConfig(cfg), Keys: awsports.Key(manifest.KeyARN)},
			Scope:       values.Scope{Project: manifest.Slug, Class: edge.Class(manifest.Class)},
			Environment: manifest.Environment,
		},
		cells: manifestCells(manifest),
		links: manifest.Links,
	}, manifestKeys(manifest), manifest.Links, nil), nil
}

func manifestKeys(m live.Manifest) []string {
	keys := make([]string, 0, len(m.Keys)+len(m.Links))
	for _, k := range m.Keys {
		keys = append(keys, k.Key)
	}
	for _, l := range m.Links {
		keys = append(keys, l.Key)
	}
	return keys
}

func manifestCells(m live.Manifest) []values.Cell {
	cells := make([]values.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, values.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}
