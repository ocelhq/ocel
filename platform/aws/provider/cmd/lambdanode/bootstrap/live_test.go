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

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

func TestLiveValues(t *testing.T) {
	t.Run("start kicks the fetch off without waiting on it", func(t *testing.T) {
		fetcher := &scriptedFetcher{release: make(chan struct{}), results: []fetchResult{{values: map[string]string{"DB_PASSWORD": "hunter2"}}}}
		out := &sink{}
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, nil)
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
	})

	t.Run("a generation holding nothing is pushed as an empty map", func(t *testing.T) {
		out := &sink{}
		l := newLiveValues(resolves(nil), []string{"DB_PASSWORD"}, nil, nil)
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
	})

	t.Run("the first generation is pushed in the shape node decodes", func(t *testing.T) {
		out := &sink{}
		l := newLiveValues(resolves(map[string]string{"DB_PASSWORD": "hunter2", "API_KEY": "sk-live"}), []string{"DB_PASSWORD", "API_KEY"}, nil, nil)
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
	})

	t.Run("a generation resolved before node connects is delivered on connect", func(t *testing.T) {
		l := newLiveValues(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

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
	})

	t.Run("an invocation within the bound costs no fetch", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		fetcher := resolves(map[string]string{"DB_PASSWORD": "hunter2"})
		out := &sink{}
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
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
	})

	t.Run("a rotation is picked up in the background and pushed as the next generation", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		fetcher := resolves(
			map[string]string{"DB_PASSWORD": "hunter2"},
			map[string]string{"DB_PASSWORD": "rotated"},
		)
		out := &sink{}
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
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
	})

	t.Run("an invocation does not stack refreshes on one already in flight", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		fetcher := resolves(map[string]string{"DB_PASSWORD": "hunter2"})
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
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
	})

	t.Run("a prefetch that cannot reach the store fails init", func(t *testing.T) {
		l := newLiveValues(fails(errors.New("dial tcp: connection refused")), []string{"DB_PASSWORD"}, nil, nil)
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
	})

	t.Run("a failed refresh pushes nothing and keeps the last generation", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		fetcher := &scriptedFetcher{results: []fetchResult{
			{values: map[string]string{"DB_PASSWORD": "hunter2"}},
			{err: errors.New("dial tcp: connection refused")},
		}}
		out := &sink{}
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
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
	})

	t.Run("tells node which keys to expect a push for", func(t *testing.T) {
		l := newLiveValues(resolves(map[string]string{}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)

		got := l.declaredEnv()
		want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("declaredEnv() = %q, want %q", got, want)
		}
	})

	t.Run("says nothing for a function that declares none", func(t *testing.T) {
		for name, l := range map[string]*liveValues{
			"no live manifest at all": nil,
			"a cache naming no keys":  newLiveValues(resolves(map[string]string{}), nil, nil, nil),
		} {
			t.Run(name, func(t *testing.T) {
				if got := l.declaredEnv(); len(got) != 0 {
					t.Errorf("declaredEnv() = %q, want no entry at all: naming the variable is what makes node wait", got)
				}
			})
		}
	})
}

func record(t *testing.T, link *linksv1.Link) string {
	t.Helper()
	encoded, err := vars.EncodeLink(link)
	if err != nil {
		t.Fatalf("EncodeLink: %v", err)
	}
	return string(encoded)
}

func postgresRecord(t *testing.T, password string) string {
	t.Helper()
	return record(t, &linksv1.Link{
		Name:       "db--main",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "db.host", Port: 5432, Database: "ocel", Username: "ocel", Password: password}},
	})
}

func bucketRecord(t *testing.T, bucket string) string {
	t.Helper()
	return record(t, &linksv1.Link{
		Name:       "db--main",
		Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: bucket}},
	})
}

func decodeLink(t *testing.T, raw string) *linksv1.Link {
	t.Helper()
	link := &linksv1.Link{}
	if err := protojson.Unmarshal([]byte(raw), link); err != nil {
		t.Fatalf("the child was handed %q, which it cannot parse: %v", raw, err)
	}
	return link
}

func postgresLink() live.Link {
	return live.Link{
		Name: "db--main",
		Key:  "OCEL_RESOURCE_POSTGRES_main",
		Type: linksv1.LinkType_LINK_TYPE_POSTGRES,
	}
}

func TestLinkColdStart(t *testing.T) {
	t.Run("a link's value arrives at cold start as the record the app reads", func(t *testing.T) {
		link := postgresLink()
		fetcher := resolves(map[string]string{
			link.Key:      postgresRecord(t, "s3cr3t"),
			"DB_PASSWORD": "hunter2",
		})
		out := &sink{}
		l := newLiveValues(fetcher, []string{link.Key, "DB_PASSWORD"}, []live.Link{link}, nil)
		l.attach(out)

		if err := l.join(l.start(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		msgs := out.messages(t)
		if len(msgs) != 1 {
			t.Fatalf("pushed %d messages, want the cold start's generation", len(msgs))
		}
		handed := decodeLink(t, msgs[0].Values[link.Key])
		if handed.GetPostgres().GetPassword() != "s3cr3t" || handed.GetPostgres().GetHost() != "db.host" {
			t.Errorf("record = %v, want the published credential", handed)
		}
		if msgs[0].Values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("values = %v, want the user's own secrets delivered alongside", msgs[0].Values)
		}
	})

	t.Run("a credential rotated after the deploy is served at the next cold start", func(t *testing.T) {
		link := postgresLink()
		fetcher := resolves(
			map[string]string{link.Key: postgresRecord(t, "old")},
			map[string]string{link.Key: postgresRecord(t, "rotated")},
		)

		served := func() string {
			out := &sink{}
			l := newLiveValues(fetcher, []string{link.Key}, []live.Link{link}, nil)
			l.attach(out)
			if err := l.join(l.start(context.Background())); err != nil {
				t.Fatalf("join: %v", err)
			}
			msgs := out.messages(t)
			if len(msgs) != 1 {
				t.Fatalf("pushed %d messages, want the cold start's generation", len(msgs))
			}
			return msgs[0].Values[link.Key]
		}

		first := served()
		second := served()
		if !strings.Contains(first, "old") {
			t.Fatalf("first cold start served %q, want the credential as published", first)
		}
		if !strings.Contains(second, "rotated") {
			t.Errorf("second cold start served %q, want the rotated credential: the artifact never changed, so nothing but a live read can pick it up", second)
		}
	})

	t.Run("drift between the published record and this deployment fails cold start", func(t *testing.T) {
		link := postgresLink()
		for name, tc := range map[string]struct {
			values map[string]string
			names  []string
		}{
			"a record published under another type": {
				values: map[string]string{link.Key: bucketRecord(t, "shop-uploads")},
				names:  []string{"db--main", "LINK_TYPE_BUCKET", "LINK_TYPE_POSTGRES", link.Key},
			},
			"a record carrying no properties at all": {
				values: map[string]string{link.Key: record(t, &linksv1.Link{Name: "db--main"})},
				names:  []string{"db--main", "LINK_TYPE_POSTGRES", link.Key},
			},
			"no record at all": {
				values: map[string]string{"DB_PASSWORD": "hunter2"},
				names:  []string{"db--main", link.Key, "LINK_TYPE_POSTGRES"},
			},
			"a value that is not a record": {
				values: map[string]string{link.Key: "postgres://ocel@db.host:5432/ocel"},
				names:  []string{"db--main", link.Key},
			},
		} {
			t.Run(name, func(t *testing.T) {
				out := &sink{}
				l := newLiveValues(resolves(tc.values), []string{link.Key}, []live.Link{link}, nil)
				l.attach(out)

				err := l.join(l.start(context.Background()))
				if err == nil {
					t.Fatal("join = nil, want a cold start refused rather than an app handed a shape it cannot read")
				}
				if !errors.Is(err, live.ErrDrift) {
					t.Errorf("error = %v, want it named as drift", err)
				}
				for _, want := range tc.names {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %v, want it to name %q", err, want)
					}
				}
				if msgs := out.messages(t); len(msgs) != 0 {
					t.Errorf("pushed %+v, want nothing delivered to an app whose links drifted", msgs)
				}
			})
		}
	})

	t.Run("drift is reported as itself and not as node never starting", func(t *testing.T) {
		const budget = 5 * time.Second
		link := postgresLink()

		spawn := func(_ []string, budget time.Duration, _ func(io.Writer), abandon <-chan struct{}) (*Membrane, error) {
			select {
			case <-abandon:
			case <-time.After(budget):
			}
			return nil, fmt.Errorf("node did not signal ready within %s", budget)
		}

		l := newLiveValues(
			resolves(map[string]string{link.Key: bucketRecord(t, "x")}),
			[]string{link.Key},
			[]live.Link{link},
			nil,
		)

		_, err := bringUp(spawn, l, l.start(context.Background()), nil, budget)
		if err == nil {
			t.Fatal("bringUp = nil, want init refused")
		}
		if !strings.Contains(err.Error(), "LINK_TYPE_BUCKET") {
			t.Errorf("error = %v, want the drift named", err)
		}
		if strings.Contains(err.Error(), "did not signal ready") {
			t.Errorf("error = %v, which reports the symptom and buries the cause", err)
		}
	})

	t.Run("drift found on a refresh keeps the last good generation serving", func(t *testing.T) {
		link := postgresLink()
		clock := time.Unix(1_700_000_000, 0)
		good := postgresRecord(t, "good")
		fetcher := resolves(
			map[string]string{link.Key: good},
			map[string]string{link.Key: bucketRecord(t, "shop-uploads")},
		)
		out := &sink{}
		l := newLiveValues(fetcher, []string{link.Key}, []live.Link{link}, func() time.Time { return clock })
		l.attach(out)

		if err := l.join(l.start(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(liveStalenessBound)
		l.refreshIfStale(context.Background())
		eventually(t, "the drifting refresh to run", func() bool { return fetcher.count() == 2 })

		consistently(t, "a warm process pushed a generation it could not conform", func() bool { return len(out.messages(t)) == 1 })
		if msgs := out.messages(t); msgs[0].Values[link.Key] != good {
			t.Errorf("serving %+v, want the last generation that conformed", msgs[0])
		}
	})
}

func TestBringUp(t *testing.T) {
	t.Run("the spawn runs beside the prefetch rather than behind it", func(t *testing.T) {
		fetcher := &scriptedFetcher{release: make(chan struct{}), results: []fetchResult{{values: map[string]string{"DB_PASSWORD": "hunter2"}}}}
		out := &sink{}
		l := newLiveValues(fetcher, []string{"DB_PASSWORD"}, nil, nil)
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
	})

	t.Run("a failed prefetch is reported as the store error not as node never starting", func(t *testing.T) {
		const budget = 5 * time.Second

		spawn := func(_ []string, budget time.Duration, _ func(io.Writer), abandon <-chan struct{}) (*Membrane, error) {
			select {
			case <-abandon:
			case <-time.After(budget):
			}
			return nil, fmt.Errorf("node did not signal ready within %s", budget)
		}

		l := newLiveValues(fails(errors.New("AccessDeniedException: dynamodb:Query")), []string{"DB_PASSWORD"}, nil, nil)

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
	})
}

func TestResolveLiveValues(t *testing.T) {
	t.Run("a function with no manifest builds nothing", func(t *testing.T) {
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
	})

	t.Run("addresses each link by the partition its pair lives in", func(t *testing.T) {
		manifest := live.Manifest{
			Slug:        "shop",
			Table:       "ocel-vars",
			KeyARN:      "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:       "preview",
			Environment: "pr-42",
			Links: []live.Link{
				{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"},
				{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads"},
			},
		}

		names := linkNames(manifest.Links)
		if len(names) != 2 {
			t.Fatalf("names = %v, want one per link", names)
		}
		for i, want := range manifest.Links {
			if names[i] != want.Name {
				t.Errorf("name %q, want the link %q the record is published under", names[i], want.Name)
			}
		}

		values, err := merged(nil, manifest.Links, []vars.PublishedRecord{
			{Link: &linksv1.Link{Name: "db--main", Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "h", Database: "d", Username: "u"}}}},
			{Link: &linksv1.Link{Name: "bucket--uploads", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "shop-uploads"}}}},
		})
		if err != nil {
			t.Fatalf("merged: %v", err)
		}
		for _, l := range manifest.Links {
			if values[l.Key] == "" {
				t.Errorf("%s reached the child under no key; the record is filed under the key the app reads", l.Name)
			}
		}

		keys := manifestKeys(manifest)
		if len(keys) != 2 {
			t.Fatalf("declared keys = %v, want the link keys named to the child", keys)
		}
	})

	t.Run("reads the pinned coordinates and never the sentinels", func(t *testing.T) {
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
	})

	t.Run("a preview reads its own override beside the class wide value", func(t *testing.T) {
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
	})

	t.Run("production asks for one cell per key", func(t *testing.T) {
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
	})

	t.Run("an unreadable manifest is an init failure", func(t *testing.T) {
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
	})

	t.Run("a manifest naming no keys builds nothing", func(t *testing.T) {
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
	})

	t.Run("declares exactly the pinned keys", func(t *testing.T) {
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
	})
}

func TestMerged(t *testing.T) {
	links := []live.Link{{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"}}
	published := &linksv1.Link{
		Name:       "db--main",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "ocel", Port: 5432}},
	}
	records := []vars.PublishedRecord{{Link: published}}

	t.Run("a link is never shadowed by a secret that shares its name", func(t *testing.T) {
		secret := vars.Value{
			Metadata:  vars.Metadata{Coordinate: vars.Coordinate{Slug: "shop", Key: "OCEL_RESOURCE_POSTGRES_main"}},
			Plaintext: "postgres://mine",
		}

		got, err := merged([]vars.Value{secret}, links, records)
		if err != nil {
			t.Fatalf("merged: %v", err)
		}
		if handed := decodeLink(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the record ocel published for the link; a secret the user named the same way must not stand in for a resource's own credential", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})

	t.Run("carries both when they name different keys", func(t *testing.T) {
		secret := vars.Value{
			Metadata:  vars.Metadata{Coordinate: vars.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}},
			Plaintext: "sk_live",
		}

		got, err := merged([]vars.Value{secret}, links, records)
		if err != nil {
			t.Fatalf("merged: %v", err)
		}
		if got["STRIPE_API_KEY"] != "sk_live" || len(got) != 2 {
			t.Errorf("merged = %v, want the secret beside the link and nothing else", got)
		}
		if handed := decodeLink(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the published link", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})
}

func TestResolved(t *testing.T) {
	t.Run("an override wins for its own environment and nothing else changes", func(t *testing.T) {
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
	})
}

func TestChildEnv(t *testing.T) {
	t.Run("carries the live declaration beside the delivered class", func(t *testing.T) {
		bakedEnv := []string{"OCEL_VAR_STRIPE_KEY=sk_baked"}
		l := newLiveValues(resolves(map[string]string{}), []string{"DB_PASSWORD"}, nil, nil)

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
	})

	t.Run("never carries a live plaintext", func(t *testing.T) {
		const dbPassword = "pg-plaintext-must-not-be-exported"
		const sessionSecret = "session-plaintext-must-not-be-exported"
		secrets := []string{dbPassword, sessionSecret}

		l := newLiveValues(resolves(map[string]string{
			"DB_PASSWORD":    dbPassword,
			"SESSION_SECRET": sessionSecret,
		}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)
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
	})
}

func TestLiveStalenessBound(t *testing.T) {
	t.Run("is sixty seconds", func(t *testing.T) {
		if liveStalenessBound != 60*time.Second {
			t.Errorf("liveStalenessBound = %s, want 60s", liveStalenessBound)
		}
	})
}

func TestGrantLag(t *testing.T) {
	bound := func(granted int64) live.Link {
		link := postgresLink()
		link.Granted = granted
		return link
	}
	published := func(version int64) []vars.PublishedRecord {
		return []vars.PublishedRecord{{Link: &linksv1.Link{Name: "main", Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{}}}, Version: version}}
	}

	t.Run("names the publishes an app's grants are behind", func(t *testing.T) {
		got := grantLag([]live.Link{bound(3)}, published(5))
		if len(got) != 1 {
			t.Fatalf("grantLag = %v, want the lag reported once", got)
		}
		for _, want := range []string{"main", "2 more time", "version 3", "version 5"} {
			if !strings.Contains(got[0].Message, want) {
				t.Errorf("report = %q, want it to carry %q", got[0].Message, want)
			}
		}
	})

	t.Run("says nothing while the grants match the record", func(t *testing.T) {
		if got := grantLag([]live.Link{bound(5)}, published(5)); len(got) != 0 {
			t.Errorf("grantLag = %v, want silence when the running grants came from the live version", got)
		}
	})

	t.Run("says nothing for a link this deploy provisioned and granted in one pass", func(t *testing.T) {
		if got := grantLag([]live.Link{postgresLink()}, published(9)); len(got) != 0 {
			t.Errorf("grantLag = %v, want no lag where publish and grant are the same act", got)
		}
	})

	t.Run("repeats itself only when the record moves again", func(t *testing.T) {
		fetcher := &storeFetcher{links: []live.Link{bound(3)}}

		if got := fetcher.unreportedGrantLag(published(5)); len(got) != 1 {
			t.Fatalf("first refresh reported %v, want the lag once", got)
		}
		if got := fetcher.unreportedGrantLag(published(5)); len(got) != 0 {
			t.Errorf("second refresh reported %v, want a standing lag reported once, not on every refresh", got)
		}
		if got := fetcher.unreportedGrantLag(published(6)); len(got) != 1 {
			t.Errorf("a further publish reported %v, want the widened lag named again", got)
		}
	})
}
