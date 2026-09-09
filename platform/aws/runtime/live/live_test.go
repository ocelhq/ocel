package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
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

func (s *sink) messages(t *testing.T) []valuesMsg {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]valuesMsg, 0, len(s.lines))
	for _, line := range s.lines {
		var msg valuesMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("the runtime pushed %q, which node cannot decode: %v", line, err)
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
		l := newValues(fetcher, []string{"DB_PASSWORD"}, nil, nil)
		l.Attach(out)

		done := l.Prefetch(context.Background())
		consistently(t, "a generation was pushed while the fetch was still held", func() bool { return len(out.messages(t)) == 0 })

		close(fetcher.release)
		if err := l.Join(done); err != nil {
			t.Fatalf("join: %v", err)
		}
		if msgs := out.messages(t); len(msgs) != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("messages = %+v, want the released fetch's generation", msgs)
		}
	})

	t.Run("a generation holding nothing is pushed as an empty map", func(t *testing.T) {
		out := &sink{}
		l := newValues(resolves(nil), []string{"DB_PASSWORD"}, nil, nil)
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
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
		l := newValues(resolves(map[string]string{"DB_PASSWORD": "hunter2", "API_KEY": "sk-live"}), []string{"DB_PASSWORD", "API_KEY"}, nil, nil)
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		msgs := out.messages(t)
		if len(msgs) != 1 {
			t.Fatalf("pushed %d messages, want exactly the first generation", len(msgs))
		}
		if msgs[0].Type != "Values" {
			t.Errorf("type = %q, want %q", msgs[0].Type, "Values")
		}
		if msgs[0].Generation != 1 {
			t.Errorf("generation = %d, want 1", msgs[0].Generation)
		}
		if msgs[0].Values["DB_PASSWORD"] != "hunter2" || msgs[0].Values["API_KEY"] != "sk-live" {
			t.Errorf("values = %v, want both resolved keys", msgs[0].Values)
		}
	})

	t.Run("a generation resolved before node connects is delivered on connect", func(t *testing.T) {
		l := newValues(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		out := &sink{}
		if msgs := out.messages(t); len(msgs) != 0 {
			t.Fatalf("pushed %d messages before node connected", len(msgs))
		}
		l.Attach(out)

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
		l := newValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(stalenessBound - time.Second)
		for range 5 {
			l.Refresh(context.Background())
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
		l := newValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		fetcher.release = make(chan struct{})
		clock = clock.Add(stalenessBound)

		start := time.Now()
		l.Refresh(context.Background())
		if blocked := time.Since(start); blocked >= fetchBudget {
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
		l := newValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(&sink{})

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		fetcher.release = make(chan struct{})
		clock = clock.Add(10 * stalenessBound)
		for range 5 {
			l.Refresh(context.Background())
		}
		eventually(t, "the refresh to reach the store", func() bool { return fetcher.count() >= 2 })

		consistently(t, "an invocation stacked a second refresh on the one already in flight", func() bool { return fetcher.count() == 2 })
		close(fetcher.release)
	})

	t.Run("a prefetch that cannot reach the store fails init", func(t *testing.T) {
		l := newValues(fails(errors.New("dial tcp: connection refused")), []string{"DB_PASSWORD"}, nil, nil)
		out := &sink{}
		l.Attach(out)

		err := l.Join(l.Prefetch(context.Background()))
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
		l := newValues(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(stalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the failing refresh to run", func() bool { return fetcher.count() == 2 })

		msgs := out.messages(t)
		if len(msgs) != 1 {
			t.Fatalf("pushed %+v, want only the generation that resolved", msgs)
		}
		if msgs[0].Generation != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("message = %+v, want the last good generation untouched", msgs[0])
		}

		clock = clock.Add(stalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the refresh to be retried", func() bool { return fetcher.count() == 3 })
	})

	t.Run("tells node which keys to expect a push for", func(t *testing.T) {
		l := newValues(resolves(map[string]string{}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)

		got := l.Env()
		want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("declaredEnv() = %q, want %q", got, want)
		}
	})

	t.Run("says nothing for a function that declares none", func(t *testing.T) {
		for name, l := range map[string]*Values{
			"no live manifest at all": nil,
			"a cache naming no keys":  newValues(resolves(map[string]string{}), nil, nil, nil),
		} {
			t.Run(name, func(t *testing.T) {
				if got := l.Env(); len(got) != 0 {
					t.Errorf("declaredEnv() = %q, want no entry at all: naming the variable is what makes node wait", got)
				}
			})
		}
	})
}

func record(t *testing.T, link *linksv1.Link) string {
	t.Helper()
	encoded, err := protojson.Marshal(link)
	if err != nil {
		t.Fatalf("render the link: %v", err)
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

func postgresLink() vars.Link {
	return vars.Link{
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
		l := newValues(fetcher, []string{link.Key, "DB_PASSWORD"}, []vars.Link{link}, nil)
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
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
			l := newValues(fetcher, []string{link.Key}, []vars.Link{link}, nil)
			l.Attach(out)
			if err := l.Join(l.Prefetch(context.Background())); err != nil {
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
				l := newValues(resolves(tc.values), []string{link.Key}, []vars.Link{link}, nil)
				l.Attach(out)

				err := l.Join(l.Prefetch(context.Background()))
				if err == nil {
					t.Fatal("join = nil, want a cold start refused rather than an app handed a shape it cannot read")
				}
				if !errors.Is(err, vars.ErrDrift) {
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

	t.Run("drift found on a refresh keeps the last good generation serving", func(t *testing.T) {
		link := postgresLink()
		clock := time.Unix(1_700_000_000, 0)
		good := postgresRecord(t, "good")
		fetcher := resolves(
			map[string]string{link.Key: good},
			map[string]string{link.Key: bucketRecord(t, "shop-uploads")},
		)
		out := &sink{}
		l := newValues(fetcher, []string{link.Key}, []vars.Link{link}, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(stalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the drifting refresh to run", func() bool { return fetcher.count() == 2 })

		consistently(t, "a warm process pushed a generation it could not conform", func() bool { return len(out.messages(t)) == 1 })
		if msgs := out.messages(t); msgs[0].Values[link.Key] != good {
			t.Errorf("serving %+v, want the last generation that conformed", msgs[0])
		}
	})
}

func TestResolveLiveValues(t *testing.T) {
	t.Run("a function with no manifest builds nothing", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
			t.Setenv(name, "")
		}

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l != nil {
			t.Fatal("a function with no live manifest built a store client anyway")
		}

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Errorf("start/join on a function with no live values = %v, want nil", err)
		}
		l.Attach(&sink{})
		l.Refresh(context.Background())
	})

	t.Run("addresses each link by the partition its pair lives in", func(t *testing.T) {
		manifest := vars.Manifest{
			Slug:        "shop",
			Table:       "ocel-vars",
			KeyARN:      "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:       "preview",
			Environment: "pr-42",
			Links: []vars.Link{
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

		values := merged(nil, manifest.Links, []values.Published{
			publishedRecord(t, &linksv1.Link{Name: "db--main", Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "h", Database: "d", Username: "u"}}}, 1),
			publishedRecord(t, &linksv1.Link{Name: "bucket--uploads", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "shop-uploads"}}}, 1),
		})
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
		manifest := vars.Manifest{
			Slug:   "shop",
			Table:  "ocel-vars",
			KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:  "production",
			Keys:   []vars.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}

		cells := manifestCells(manifest)
		if len(cells) != 2 {
			t.Fatalf("cells = %+v, want one per pinned key", cells)
		}
		if cells[0].Folder != "" {
			t.Errorf("root key folder = %q, want empty: the store owns the root sentinel", cells[0].Folder)
		}
		if cells[1].Folder != "/web" {
			t.Errorf("scoped key folder = %q, want the pinned folder", cells[1].Folder)
		}
	})

	t.Run("a preview names each cell once and reads its environment through the reader", func(t *testing.T) {
		manifest := vars.Manifest{
			Slug:        "shop",
			Environment: "pr-42",
			Keys:        []vars.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}

		want := []values.Cell{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}}
		if cells := manifestCells(manifest); !reflect.DeepEqual(cells, want) {
			t.Errorf("cells = %+v, want %+v: the override is the reader's environment, not a cell of its own", cells, want)
		}
	})

	t.Run("production asks for one cell per key", func(t *testing.T) {
		cells := manifestCells(vars.Manifest{
			Slug: "shop",
			Keys: []vars.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		})

		if len(cells) != 2 {
			t.Fatalf("cells = %+v, want one per pinned key", cells)
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
				path := filepath.Join(root, vars.FilePath)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				plant(t, path)

				if _, err := Resolve(context.Background(), root); err == nil {
					t.Fatal("Resolve absorbed a manifest it could not read")
				}
			})
		}
	})

	t.Run("a manifest naming no keys builds nothing", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, vars.FilePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"slug":"shop","table":"ocel-vars","keyArn":"arn","class":"production","keys":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
			t.Setenv(name, "")
		}

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l != nil {
			t.Fatal("a manifest naming no keys built a store client anyway")
		}
	})

	t.Run("declares exactly the pinned keys", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, vars.FilePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := vars.Manifest{
			Slug:   "shop",
			Table:  "ocel-vars",
			KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:  "production",
			Keys:   []vars.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}
		rendered, err := vars.Render(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AWS_REGION", "us-east-1")

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		got := l.Env()
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
	links := []vars.Link{{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"}}
	published := &linksv1.Link{
		Name:       "db--main",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "ocel", Port: 5432}},
	}
	records := []values.Published{publishedRecord(t, published, 1)}

	t.Run("a link is never shadowed by a secret that shares its name", func(t *testing.T) {
		got := merged(map[string]string{"OCEL_RESOURCE_POSTGRES_main": "postgres://mine"}, links, records)
		if handed := decodeLink(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the record ocel published for the link; a secret the user named the same way must not stand in for a resource's own credential", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})

	t.Run("carries both when they name different keys", func(t *testing.T) {
		got := merged(map[string]string{"STRIPE_API_KEY": "sk_live"}, links, records)
		if got["STRIPE_API_KEY"] != "sk_live" || len(got) != 2 {
			t.Errorf("merged = %v, want the secret beside the link and nothing else", got)
		}
		if handed := decodeLink(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the published link", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})
}

func publishedRecord(t *testing.T, link *linksv1.Link, version int64) values.Published {
	t.Helper()
	encoded, err := protojson.Marshal(link)
	if err != nil {
		t.Fatalf("render the link: %v", err)
	}
	return values.Published{Name: link.GetName(), Value: encoded, Version: version}
}

func TestEnv(t *testing.T) {
	t.Run("declares the live keys the child must ask for", func(t *testing.T) {
		l := newValues(resolves(map[string]string{}), []string{"DB_PASSWORD"}, nil, nil)

		if got := l.Env(); !slices.Equal(got, []string{"OCEL_LIVE_KEYS=DB_PASSWORD"}) {
			t.Errorf("Env = %q, want the declaration alone", got)
		}
		if got := (*Values)(nil).Env(); got != nil {
			t.Errorf("Env for a function with no live values = %q, want nothing", got)
		}
	})

	t.Run("never carries a live plaintext", func(t *testing.T) {
		const dbPassword = "pg-plaintext-must-not-be-exported"
		const sessionSecret = "session-plaintext-must-not-be-exported"
		secrets := []string{dbPassword, sessionSecret}

		l := newValues(resolves(map[string]string{
			"DB_PASSWORD":    dbPassword,
			"SESSION_SECRET": sessionSecret,
		}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("prefetch: %v", err)
		}

		before := os.Environ()
		got := l.Env()

		for _, entry := range got {
			for _, secret := range secrets {
				if strings.Contains(entry, secret) {
					t.Errorf("Env put %q in the child's environment, which discloses a live plaintext to anything that reads that environment", entry)
				}
			}
		}
		if want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}; !slices.Equal(got, want) {
			t.Errorf("Env = %q, want exactly %q", got, want)
		}

		for _, entry := range os.Environ() {
			for _, secret := range secrets {
				if strings.Contains(entry, secret) {
					t.Errorf("Env set %q on this process's own environment", entry)
				}
			}
		}
		if !slices.Equal(os.Environ(), before) {
			t.Errorf("Env changed this process's environment; it may only compose a slice")
		}
	})
}

func TestLiveStalenessBound(t *testing.T) {
	t.Run("is sixty seconds", func(t *testing.T) {
		if stalenessBound != 60*time.Second {
			t.Errorf("stalenessBound = %s, want 60s", stalenessBound)
		}
	})
}

func TestGrantLag(t *testing.T) {
	bound := func(granted int64) vars.Link {
		link := postgresLink()
		link.Granted = granted
		return link
	}
	published := func(version int64) []values.Published {
		return []values.Published{{Name: "main", Version: version}}
	}

	t.Run("names the publishes an app's grants are behind", func(t *testing.T) {
		got := grantLag([]vars.Link{bound(3)}, published(5))
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
		if got := grantLag([]vars.Link{bound(5)}, published(5)); len(got) != 0 {
			t.Errorf("grantLag = %v, want silence when the running grants came from the live version", got)
		}
	})

	t.Run("says nothing for a link this deploy provisioned and granted in one pass", func(t *testing.T) {
		if got := grantLag([]vars.Link{postgresLink()}, published(9)); len(got) != 0 {
			t.Errorf("grantLag = %v, want no lag where publish and grant are the same act", got)
		}
	})

	t.Run("repeats itself only when the record moves again", func(t *testing.T) {
		fetcher := &storeFetcher{links: []vars.Link{bound(3)}}

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
