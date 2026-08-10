package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cloud/aws/vars"
	"github.com/ocelhq/ocel/cloud/aws/vars/live"
)

type scriptedFetcher struct {
	release chan struct{}

	mu      sync.Mutex
	calls   int
	results []fetchResult
}

type fetchResult struct {
	values map[string]string
	err    error
}

func (f *scriptedFetcher) fetchLive(ctx context.Context) (map[string]string, error) {
	f.mu.Lock()
	n := f.calls
	f.calls++
	f.mu.Unlock()

	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.results) {
		n = len(f.results) - 1
	}
	return f.results[n].values, f.results[n].err
}

func (f *scriptedFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func resolves(values ...map[string]string) *scriptedFetcher {
	f := &scriptedFetcher{}
	for _, v := range values {
		f.results = append(f.results, fetchResult{values: v})
	}
	return f
}

func fails(err error) *scriptedFetcher {
	return &scriptedFetcher{results: []fetchResult{{err: err}}}
}

type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func (s *sink) raw() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

func (s *sink) messages(t *testing.T) []liveValuesMsg {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]liveValuesMsg, 0, len(s.lines))
	for _, line := range s.lines {
		var msg liveValuesMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("the membrane pushed %q, which node cannot decode: %v", line, err)
		}
		out = append(out, msg)
	}
	return out
}

func consistently(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s", why)
		}
		time.Sleep(time.Millisecond)
	}
}

func eventually(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestLiveValues_StartKicksTheFetchOffWithoutWaitingOnIt(t *testing.T) {
	fetcher := &scriptedFetcher{release: make(chan struct{}), results: []fetchResult{{values: map[string]string{"DB_PASSWORD": "hunter2"}}}}
	out := &sink{}
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil)
	l.attach(out)

	done := l.start(context.Background())
	consistently(t, "a generation was pushed while the fetch was still held", func() bool { return len(out.messages(t)) == 0 })

	close(fetcher.release)
	if err := l.join(done); err != nil {
		t.Fatalf("join: %v", err)
	}
	if msgs := out.messages(t); len(msgs) != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("messages = %+v, want the released fetch's generation", msgs)
	}
}

func TestBringUp_TheSpawnRunsBesideThePrefetchRatherThanBehindIt(t *testing.T) {
	fetcher := &scriptedFetcher{release: make(chan struct{}), results: []fetchResult{{values: map[string]string{"DB_PASSWORD": "hunter2"}}}}
	out := &sink{}
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil)
	prefetch := l.start(context.Background())

	spawn := func(_ []string, _ time.Duration, onControl func(io.Writer), _ <-chan struct{}) (*Membrane, error) {
		close(fetcher.release)
		onControl(out)
		return &Membrane{}, nil
	}

	membrane, err := bringUp(spawn, l, prefetch, nil, time.Minute)
	if err != nil {
		t.Fatalf("bringUp: %v", err)
	}
	if membrane.live != l {
		t.Error("the membrane was not given the cache the invocations it serves refresh from")
	}
	if msgs := out.messages(t); len(msgs) != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("messages = %+v, want the prefetched generation delivered to the child", msgs)
	}
}

func TestBringUp_AFailedPrefetchIsReportedAsTheStoreErrorNotAsNodeNeverStarting(t *testing.T) {
	const budget = 5 * time.Second

	spawn := func(_ []string, budget time.Duration, _ func(io.Writer), abandon <-chan struct{}) (*Membrane, error) {
		select {
		case <-abandon:
		case <-time.After(budget):
		}
		return nil, fmt.Errorf("node did not signal ready within %s", budget)
	}

	l := newLiveValues(fails(errors.New("AccessDeniedException: dynamodb:Query")), []string{"DB_PASSWORD"}, nil)

	start := time.Now()
	_, err := bringUp(spawn, l, l.start(context.Background()), nil, budget)
	took := time.Since(start)

	if err == nil {
		t.Fatal("bringUp = nil, want a function that cannot resolve a value it declared refused")
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("error = %v, want what the store said: node timing out is the symptom, not the cause", err)
	}
	if strings.Contains(err.Error(), "did not signal ready") {
		t.Errorf("error = %v, which reports the symptom and buries the cause", err)
	}
	if took >= budget {
		t.Errorf("init spent %s of a %s budget waiting on a child that was never going to come up", took, budget)
	}
}

func TestLiveValues_AGenerationHoldingNothingIsPushedAsAnEmptyMap(t *testing.T) {
	out := &sink{}
	l := newLiveValues(resolves(nil), []string{"DB_PASSWORD"}, nil)
	l.attach(out)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	lines := out.raw()
	if len(lines) != 1 {
		t.Fatalf("pushed %q, want the one generation", lines)
	}
	if !strings.Contains(lines[0], `"values":{}`) {
		t.Errorf("pushed %s, want an empty object for values: node discards null and keeps waiting", lines[0])
	}
}

func TestLiveValues_TheFirstGenerationIsPushedInTheShapeNodeDecodes(t *testing.T) {
	out := &sink{}
	l := newLiveValues(resolves(map[string]string{"DB_PASSWORD": "hunter2", "API_KEY": "sk-live"}), []string{"DB_PASSWORD", "API_KEY"}, nil)
	l.attach(out)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	msgs := out.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("pushed %d messages, want exactly the first generation", len(msgs))
	}
	if msgs[0].Type != "liveValues" {
		t.Errorf("type = %q, want %q", msgs[0].Type, "liveValues")
	}
	if msgs[0].Generation != 1 {
		t.Errorf("generation = %d, want 1", msgs[0].Generation)
	}
	if msgs[0].Values["DB_PASSWORD"] != "hunter2" || msgs[0].Values["API_KEY"] != "sk-live" {
		t.Errorf("values = %v, want both resolved keys", msgs[0].Values)
	}
}

func TestLiveValues_AGenerationResolvedBeforeNodeConnectsIsDeliveredOnConnect(t *testing.T) {
	l := newLiveValues(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	out := &sink{}
	if msgs := out.messages(t); len(msgs) != 0 {
		t.Fatalf("pushed %d messages before node connected", len(msgs))
	}
	l.attach(out)

	msgs := out.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("pushed %d messages on connect, want the resolved generation", len(msgs))
	}
	if msgs[0].Generation != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("message = %+v, want generation 1 carrying the resolved value", msgs[0])
	}
}

func TestLiveValues_AnInvocationWithinTheBoundCostsNoFetch(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	fetcher := resolves(map[string]string{"DB_PASSWORD": "hunter2"})
	out := &sink{}
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, func() time.Time { return clock })
	l.attach(out)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	clock = clock.Add(liveStalenessBound - time.Second)
	for range 5 {
		l.refreshIfStale(context.Background())
	}

	consistently(t, "an invocation inside the bound read the store again", func() bool { return fetcher.count() == 1 })
	if msgs := out.messages(t); len(msgs) != 1 {
		t.Errorf("pushed %d messages, want only the first generation", len(msgs))
	}
}

func TestLiveValues_ARotationIsPickedUpInTheBackgroundAndPushedAsTheNextGeneration(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	fetcher := resolves(
		map[string]string{"DB_PASSWORD": "hunter2"},
		map[string]string{"DB_PASSWORD": "rotated"},
	)
	out := &sink{}
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, func() time.Time { return clock })
	l.attach(out)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	fetcher.release = make(chan struct{})
	clock = clock.Add(liveStalenessBound)

	start := time.Now()
	l.refreshIfStale(context.Background())
	if blocked := time.Since(start); blocked >= liveFetchBudget {
		t.Errorf("the invocation blocked %s on the refresh; a refresh must never be waited on", blocked)
	}
	if msgs := out.messages(t); len(msgs) != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("messages = %+v, want the stale generation still the newest while revalidating", msgs)
	}

	close(fetcher.release)
	eventually(t, "the refreshed generation to be pushed", func() bool { return len(out.messages(t)) == 2 })

	msgs := out.messages(t)
	if msgs[1].Generation != 2 {
		t.Errorf("generation = %d, want 2: generations must be monotonic so a late message cannot resurrect an older value", msgs[1].Generation)
	}
	if msgs[1].Values["DB_PASSWORD"] != "rotated" {
		t.Errorf("values = %v, want the rotated value", msgs[1].Values)
	}
}

func TestLiveValues_AnInvocationDoesNotStackRefreshesOnOneAlreadyInFlight(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	fetcher := resolves(map[string]string{"DB_PASSWORD": "hunter2"})
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, func() time.Time { return clock })
	l.attach(&sink{})

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	fetcher.release = make(chan struct{})
	clock = clock.Add(10 * liveStalenessBound)
	for range 5 {
		l.refreshIfStale(context.Background())
	}
	eventually(t, "the refresh to reach the store", func() bool { return fetcher.count() >= 2 })

	consistently(t, "an invocation stacked a second refresh on the one already in flight", func() bool { return fetcher.count() == 2 })
	close(fetcher.release)
}

func TestLiveValues_APrefetchThatCannotReachTheStoreFailsInit(t *testing.T) {
	l := newLiveValues(fails(errors.New("dial tcp: connection refused")), []string{"DB_PASSWORD"}, nil)
	out := &sink{}
	l.attach(out)

	err := l.join(l.start(context.Background()))
	if err == nil {
		t.Fatal("join = nil, want the unreachable store reported so init fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want it to carry what the store said", err)
	}
	if msgs := out.messages(t); len(msgs) != 0 {
		t.Errorf("pushed %+v, want nothing at all", msgs)
	}
}

func TestLiveValues_AFailedRefreshPushesNothingAndKeepsTheLastGeneration(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	fetcher := &scriptedFetcher{results: []fetchResult{
		{values: map[string]string{"DB_PASSWORD": "hunter2"}},
		{err: errors.New("dial tcp: connection refused")},
	}}
	out := &sink{}
	l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, func() time.Time { return clock })
	l.attach(out)

	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("join: %v", err)
	}

	clock = clock.Add(liveStalenessBound)
	l.refreshIfStale(context.Background())
	eventually(t, "the failing refresh to run", func() bool { return fetcher.count() == 2 })

	msgs := out.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("pushed %+v, want only the generation that resolved", msgs)
	}
	if msgs[0].Generation != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("message = %+v, want the last good generation untouched", msgs[0])
	}

	clock = clock.Add(liveStalenessBound)
	l.refreshIfStale(context.Background())
	eventually(t, "the refresh to be retried", func() bool { return fetcher.count() == 3 })
}

func TestResolveLiveValues_AFunctionWithNoManifestBuildsNothing(t *testing.T) {
	t.Setenv("LAMBDA_TASK_ROOT", t.TempDir())
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
		t.Setenv(name, "")
	}

	l, err := resolveLiveValues(context.Background())
	if err != nil {
		t.Fatalf("resolveLiveValues: %v", err)
	}
	if l != nil {
		t.Fatal("a function with no live manifest built a store client anyway")
	}

	if err := l.join(l.start(context.Background())); err != nil {
		t.Errorf("start/join on a function with no live values = %v, want nil", err)
	}
	l.attach(&sink{})
	l.refreshIfStale(context.Background())
}

func TestResolveLiveValues_ReadsThePinnedCoordinatesAndNeverTheSentinels(t *testing.T) {
	manifest := live.Manifest{
		Slug:   "shop",
		Table:  "ocel-vars",
		KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
		Class:  "production",
		Keys:   []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
	}

	cells := manifestCells(manifest)
	if len(cells) != 2 {
		t.Fatalf("cells = %+v, want one per pinned key", cells)
	}
	for _, c := range cells {
		if c.Slug != "shop" {
			t.Errorf("cell %+v does not carry the manifest's slug", c)
		}
		if c.Environment != "" {
			t.Errorf("cell %+v names an environment; the class-wide case is the store's own spelling, not one to pass in", c)
		}
	}
	if cells[0].Folder != "" {
		t.Errorf("root key folder = %q, want empty: the store owns the root sentinel", cells[0].Folder)
	}
	if cells[1].Folder != "/web" {
		t.Errorf("scoped key folder = %q, want the pinned folder", cells[1].Folder)
	}
}

func TestResolveLiveValues_APreviewReadsItsOwnOverrideBesideTheClassWideValue(t *testing.T) {
	cells := manifestCells(live.Manifest{
		Slug:        "shop",
		Environment: "pr-42",
		Keys:        []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
	})

	want := []vars.Coordinate{
		{Slug: "shop", Key: "DB_PASSWORD"},
		{Slug: "shop", Key: "DB_PASSWORD", Environment: "pr-42"},
		{Slug: "shop", Key: "SESSION_SECRET", Folder: "/web"},
		{Slug: "shop", Key: "SESSION_SECRET", Folder: "/web", Environment: "pr-42"},
	}
	if !reflect.DeepEqual(cells, want) {
		t.Errorf("cells = %+v, want %+v", cells, want)
	}
}

func TestResolveLiveValues_ProductionAsksForOneCellPerKey(t *testing.T) {
	cells := manifestCells(live.Manifest{
		Slug: "shop",
		Keys: []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
	})

	if len(cells) != 2 {
		t.Fatalf("cells = %+v, want one per pinned key", cells)
	}
	for _, c := range cells {
		if c.Environment != "" {
			t.Errorf("cell %+v names an environment production cannot have", c)
		}
	}
}

func TestResolved_AnOverrideWinsForItsOwnEnvironmentAndNothingElseChanges(t *testing.T) {
	classWide := func(key, value string) vars.Value {
		return vars.Value{Metadata: vars.Metadata{Coordinate: vars.Coordinate{Slug: "shop", Key: key}}, Plaintext: value}
	}
	override := func(key, value string) vars.Value {
		return vars.Value{Metadata: vars.Metadata{Coordinate: vars.Coordinate{Slug: "shop", Key: key, Environment: "pr-42"}}, Plaintext: value}
	}

	for name, values := range map[string][]vars.Value{
		"class-wide answered first": {classWide("DB_PASSWORD", "shared"), override("DB_PASSWORD", "mine"), classWide("SESSION_SECRET", "shared")},
		"override answered first":   {override("DB_PASSWORD", "mine"), classWide("DB_PASSWORD", "shared"), classWide("SESSION_SECRET", "shared")},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolved(values)
			want := map[string]string{"DB_PASSWORD": "mine", "SESSION_SECRET": "shared"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("resolved = %v, want %v", got, want)
			}
		})
	}
}

func TestResolveLiveValues_AnUnreadableManifestIsAnInitFailure(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"will not parse": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"cannot be read at all": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, live.FilePath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			plant(t, path)
			t.Setenv("LAMBDA_TASK_ROOT", root)

			if _, err := resolveLiveValues(context.Background()); err == nil {
				t.Fatal("resolveLiveValues absorbed a manifest it could not read")
			}
		})
	}
}

func TestResolveLiveValues_AManifestNamingNoKeysBuildsNothing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, live.FilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"slug":"shop","table":"ocel-vars","keyArn":"arn","class":"production","keys":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAMBDA_TASK_ROOT", root)
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
		t.Setenv(name, "")
	}

	l, err := resolveLiveValues(context.Background())
	if err != nil {
		t.Fatalf("resolveLiveValues: %v", err)
	}
	if l != nil {
		t.Fatal("a manifest naming no keys built a store client anyway")
	}
}

func TestLiveValues_TellsNodeWhichKeysToExpectAPushFor(t *testing.T) {
	l := newLiveValues(resolves(map[string]string{}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil)

	got := l.declaredEnv()
	want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("declaredEnv() = %q, want %q", got, want)
	}
}

func TestLiveValues_SaysNothingForAFunctionThatDeclaresNone(t *testing.T) {
	for name, l := range map[string]*liveValues{
		"no live manifest at all": nil,
		"a cache naming no keys":  newLiveValues(resolves(map[string]string{}), nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if got := l.declaredEnv(); len(got) != 0 {
				t.Errorf("declaredEnv() = %q, want no entry at all: naming the variable is what makes node wait", got)
			}
		})
	}
}

func TestResolveLiveValues_DeclaresExactlyThePinnedKeys(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, live.FilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := live.Manifest{
		Slug:   "shop",
		Table:  "ocel-vars",
		KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
		Class:  "production",
		Keys:   []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
	}
	rendered, err := live.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAMBDA_TASK_ROOT", root)
	t.Setenv("AWS_REGION", "us-east-1")

	l, err := resolveLiveValues(context.Background())
	if err != nil {
		t.Fatalf("resolveLiveValues: %v", err)
	}
	got := l.declaredEnv()
	if len(got) != 1 {
		t.Fatalf("declaredEnv() = %q, want exactly one entry", got)
	}
	if got[0] != "OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET" {
		t.Errorf("declaredEnv() = %q, want the pinned keys by bare name, in manifest order", got[0])
	}
	if strings.Contains(got[0], "/web") {
		t.Errorf("declaredEnv() = %q, which leaks a folder into the runtime", got[0])
	}
}

func TestChildEnv_CarriesTheLiveDeclarationBesideTheDeliveredClass(t *testing.T) {
	bakedEnv := []string{"OCEL_VAR_STRIPE_KEY=sk_baked"}
	l := newLiveValues(resolves(map[string]string{}), []string{"DB_PASSWORD"}, nil)

	got := childEnv(bakedEnv, l)

	for _, want := range []string{"OCEL_VAR_STRIPE_KEY=sk_baked", "OCEL_LIVE_KEYS=DB_PASSWORD"} {
		if !slices.Contains(got, want) {
			t.Errorf("childEnv = %q, missing %q", got, want)
		}
	}

	bare := childEnv(bakedEnv, nil)
	if len(bare) != 1 {
		t.Errorf("childEnv for a function with no live values = %q, want only the class delivered in the environment", bare)
	}
}

func TestChildEnv_NeverCarriesALivePlaintext(t *testing.T) {
	const dbPassword = "pg-plaintext-must-not-be-exported"
	const sessionSecret = "session-plaintext-must-not-be-exported"
	secrets := []string{dbPassword, sessionSecret}

	l := newLiveValues(resolves(map[string]string{
		"DB_PASSWORD":    dbPassword,
		"SESSION_SECRET": sessionSecret,
	}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil)
	if err := l.join(l.start(context.Background())); err != nil {
		t.Fatalf("prefetch: %v", err)
	}

	bakedEnv := []string{"OCEL_VAR_STRIPE_KEY=sk_baked"}
	before := os.Environ()

	got := childEnv(bakedEnv, l)

	for _, entry := range got {
		for _, secret := range secrets {
			if strings.Contains(entry, secret) {
				t.Errorf("childEnv put %q in the child's environment, which discloses a live plaintext to anything that reads that environment", entry)
			}
		}
	}

	want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET", "OCEL_VAR_STRIPE_KEY=sk_baked"}
	composed := slices.Sorted(slices.Values(got))
	if !slices.Equal(composed, want) {
		t.Errorf("childEnv = %q, want exactly %q", composed, want)
	}

	for _, entry := range os.Environ() {
		for _, secret := range secrets {
			if strings.Contains(entry, secret) {
				t.Errorf("childEnv set %q on this process's own environment", entry)
			}
		}
	}
	if !slices.Equal(os.Environ(), before) {
		t.Errorf("childEnv changed this process's environment; it may only compose a slice")
	}
}

func TestLiveStalenessBound_IsSixtySeconds(t *testing.T) {
	if liveStalenessBound != 60*time.Second {
		t.Errorf("liveStalenessBound = %s, want 60s", liveStalenessBound)
	}
}
