package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
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

func (f *scriptedFetcher) FetchLive(ctx context.Context) (map[string]string, error) {
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
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, nil)
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
		l := New(resolves(nil), []string{"DB_PASSWORD"}, nil, nil)
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
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2", "API_KEY": "sk-live"}), []string{"DB_PASSWORD", "API_KEY"}, nil, nil)
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
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

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
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound - time.Second)
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
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		fetcher.release = make(chan struct{})
		clock = clock.Add(StalenessBound)

		start := time.Now()
		l.Refresh(context.Background())
		if blocked := time.Since(start); blocked >= FetchBudget {
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
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(&sink{})

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		fetcher.release = make(chan struct{})
		clock = clock.Add(10 * StalenessBound)
		for range 5 {
			l.Refresh(context.Background())
		}
		eventually(t, "the refresh to reach the store", func() bool { return fetcher.count() >= 2 })

		consistently(t, "an invocation stacked a second refresh on the one already in flight", func() bool { return fetcher.count() == 2 })
		close(fetcher.release)
	})

	t.Run("a prefetch that cannot reach the store fails init", func(t *testing.T) {
		l := New(fails(errors.New("dial tcp: connection refused")), []string{"DB_PASSWORD"}, nil, nil)
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
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the failing refresh to run", func() bool { return fetcher.count() == 2 })

		msgs := out.messages(t)
		if len(msgs) != 1 {
			t.Fatalf("pushed %+v, want only the generation that resolved", msgs)
		}
		if msgs[0].Generation != 1 || msgs[0].Values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("message = %+v, want the last good generation untouched", msgs[0])
		}

		clock = clock.Add(StalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the refresh to be retried", func() bool { return fetcher.count() == 3 })
	})

	t.Run("tells node which keys to expect a push for", func(t *testing.T) {
		l := New(resolves(map[string]string{}), []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)

		got := l.Env()
		want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("Env() = %q, want %q", got, want)
		}
	})

	t.Run("says nothing for a function that declares none", func(t *testing.T) {
		for name, l := range map[string]*Values{
			"no live manifest at all": nil,
			"a cache naming no keys":  New(resolves(map[string]string{}), nil, nil, nil),
		} {
			t.Run(name, func(t *testing.T) {
				if got := l.Env(); len(got) != 0 {
					t.Errorf("Env() = %q, want no entry at all: naming the variable is what makes node wait", got)
				}
			})
		}
	})

	t.Run("names the keys and bindings it was built for", func(t *testing.T) {
		binding := postgresBinding()
		l := New(resolves(map[string]string{}), []string{"DB_PASSWORD", binding.Key}, []Binding{binding}, nil)

		if got := l.Keys(); !slices.Equal(got, []string{"DB_PASSWORD", binding.Key}) {
			t.Errorf("Keys() = %q, want the declared keys in order", got)
		}
		if got := l.Bindings(); len(got) != 1 || got[0] != binding {
			t.Errorf("Bindings() = %+v, want the declared binding", got)
		}
		if got := (*Values)(nil).Keys(); got != nil {
			t.Errorf("Keys() for a function with no live values = %q, want nothing", got)
		}
		if got := (*Values)(nil).Bindings(); got != nil {
			t.Errorf("Bindings() for a function with no live values = %+v, want nothing", got)
		}
	})
}

func TestAValueResolvedUnderNoDeclaredKeyIsTheRuntimesAloneAndReachesNoChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	values := New(resolves(map[string]string{"DATABASE_URL": "postgres://app", "ocel.store.secretAccessKey": "s3cr3t"}),
		[]string{"DATABASE_URL"}, nil, nil)
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}
	if err := values.Project(root); err != nil {
		t.Fatalf("Project() = %v", err)
	}
	if held := values.Value("ocel.store.secretAccessKey"); held != "s3cr3t" {
		t.Errorf("Value() = %q, want the credential the runtime holds for itself", held)
	}
	if _, err := os.Stat(filepath.Join(root, "ocel.store.secretAccessKey")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the undeclared value was projected into %s, where the app it fronts would read it", root)
	}
	for _, entry := range values.Env() {
		if strings.Contains(entry, "s3cr3t") {
			t.Errorf("Env() carries %q, and the app must never be handed the store's credential", entry)
		}
	}
}

func TestMissing(t *testing.T) {
	t.Run("names the keys the store held nothing for", func(t *testing.T) {
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD", "SESSION_SECRET", "API_KEY"}, nil, nil)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		if got := l.Missing(); !slices.Equal(got, []string{"SESSION_SECRET", "API_KEY"}) {
			t.Errorf("Missing() = %q, want the declared keys nothing was stored for", got)
		}
	})

	t.Run("says nothing when every declared key resolved", func(t *testing.T) {
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}
		if got := l.Missing(); len(got) != 0 {
			t.Errorf("Missing() = %q, want silence", got)
		}
	})
}

func TestProject(t *testing.T) {
	t.Run("a key is read back through the directory as the bytes the store held", func(t *testing.T) {
		const value = "hunter2\n\x00 not a line"
		root := filepath.Join(t.TempDir(), "live")
		l := New(resolves(map[string]string{"DB_PASSWORD": value}), []string{"DB_PASSWORD"}, nil, nil)

		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(root, "DB_PASSWORD"))
		if err != nil {
			t.Fatalf("read the projected key: %v", err)
		}
		if string(got) != value {
			t.Errorf("projected %q, want %q byte for byte", got, value)
		}
	})

	t.Run("a rotation is visible through the same path and the generation it replaced is gone", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		root := filepath.Join(t.TempDir(), "live")
		fetcher := resolves(
			map[string]string{"DB_PASSWORD": "hunter2"},
			map[string]string{"DB_PASSWORD": "rotated"},
		)
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, func() time.Time { return clock })

		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the rotated value to reach the directory", func() bool {
			got, err := os.ReadFile(filepath.Join(root, "DB_PASSWORD"))
			return err == nil && string(got) == "rotated"
		})

		if _, err := os.Stat(filepath.Join(root, "..1")); !os.IsNotExist(err) {
			t.Errorf("the first generation's directory is still there (%v); a replaced generation leaves no plaintext behind", err)
		}
		if _, err := os.Stat(filepath.Join(root, "..2")); err != nil {
			t.Errorf("the serving generation's directory is missing: %v", err)
		}
	})

	t.Run("projects nothing until the first generation arrives", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "live")
		fetcher := &scriptedFetcher{release: make(chan struct{}), results: []fetchResult{{values: map[string]string{"DB_PASSWORD": "hunter2"}}}}
		l := New(fetcher, []string{"DB_PASSWORD"}, nil, nil)

		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(root, "DB_PASSWORD")); err == nil {
			t.Fatal("a key read back before anything was resolved, so the app cannot tell an empty value from an unresolved one")
		}

		done := l.Prefetch(context.Background())
		close(fetcher.release)
		if err := l.Join(done); err != nil {
			t.Fatalf("join: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(root, "DB_PASSWORD"))
		if err != nil || string(got) != "hunter2" {
			t.Errorf("read %q (%v), want the generation that arrived after the projection", got, err)
		}
	})

	t.Run("a generation resolved before the projection is written on projecting", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "live")
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}
		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(root, "DB_PASSWORD"))
		if err != nil || string(got) != "hunter2" {
			t.Errorf("read %q (%v), want the generation resolved before the projection", got, err)
		}
	})

	t.Run("names the directory to the child only once there is one", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "live")
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

		if got := l.Env(); slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, "OCEL_LIVE_DIR=") }) {
			t.Fatalf("Env() = %q, which names a directory the runtime has not made", got)
		}

		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}
		if got := l.Env(); !slices.Contains(got, "OCEL_LIVE_DIR="+root) {
			t.Errorf("Env() = %q, want it to carry OCEL_LIVE_DIR=%s", got, root)
		}
	})

	t.Run("a function declaring no key projects nothing", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "live")
		l := New(resolves(map[string]string{}), nil, nil, nil)

		if err := l.Project(root); err != nil {
			t.Fatalf("Project: %v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("a directory was made for a function with nothing to put in it (%v)", err)
		}
		if err := (*Values)(nil).Project(root); err != nil {
			t.Errorf("Project on a function with no live values = %v, want nil", err)
		}
	})

	t.Run("refuses a key that would name something other than a file in the directory", func(t *testing.T) {
		for _, key := range []string{"../escape", "nested/KEY", `back\slash`, ".hidden", ""} {
			t.Run(key, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "live")
				l := New(resolves(map[string]string{}), []string{key}, nil, nil)

				err := l.Project(root)
				if err == nil {
					t.Fatalf("Project = nil, want the key %q refused rather than written outside the directory", key)
				}
				if !strings.Contains(err.Error(), root) {
					t.Errorf("error = %v, want it to name the directory the key cannot live under", err)
				}
			})
		}
	})
}

func TestLiveStalenessBound(t *testing.T) {
	t.Run("is sixty seconds", func(t *testing.T) {
		if StalenessBound != 60*time.Second {
			t.Errorf("StalenessBound = %s, want 60s", StalenessBound)
		}
	})
}

func TestKeep(t *testing.T) {
	t.Run("stops when the process it keeps values for is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		l := New(resolves(map[string]string{"DB_PASSWORD": "hunter2"}), []string{"DB_PASSWORD"}, nil, nil)

		stopped := make(chan struct{})
		go func() {
			l.Keep(ctx)
			close(stopped)
		}()
		cancel()

		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("Keep outlived the context it was given")
		}
	})
}

func record(t *testing.T, binding *bindingsv1.Binding) string {
	t.Helper()
	encoded, err := protojson.Marshal(binding)
	if err != nil {
		t.Fatalf("render the binding: %v", err)
	}
	return string(encoded)
}

func postgresRecordFor(t *testing.T, password string) string {
	t.Helper()
	return record(t, &bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.host", Port: 5432, Database: "ocel", Username: "ocel", Password: password}},
	})
}

func bucketRecord(t *testing.T, bucket string) string {
	t.Helper()
	return record(t, &bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: bucket}},
	})
}

func decodeBinding(t *testing.T, raw string) *bindingsv1.Binding {
	t.Helper()
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), binding); err != nil {
		t.Fatalf("the child was handed %q, which it cannot parse: %v", raw, err)
	}
	return binding
}

func TestBindingColdStart(t *testing.T) {
	t.Run("a binding's value arrives at cold start as the record the app reads", func(t *testing.T) {
		binding := postgresBinding()
		fetcher := resolves(map[string]string{
			binding.Key:   postgresRecordFor(t, "s3cr3t"),
			"DB_PASSWORD": "hunter2",
		})
		out := &sink{}
		l := New(fetcher, []string{binding.Key, "DB_PASSWORD"}, []Binding{binding}, nil)
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		msgs := out.messages(t)
		if len(msgs) != 1 {
			t.Fatalf("pushed %d messages, want the cold start's generation", len(msgs))
		}
		handed := decodeBinding(t, msgs[0].Values[binding.Key])
		if handed.GetPostgres().GetPassword() != "s3cr3t" || handed.GetPostgres().GetHost() != "db.host" {
			t.Errorf("record = %v, want the published credential", handed)
		}
		if msgs[0].Values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("values = %v, want the user's own secrets delivered alongside", msgs[0].Values)
		}
	})

	t.Run("a credential rotated after the deploy is served at the next cold start", func(t *testing.T) {
		binding := postgresBinding()
		fetcher := resolves(
			map[string]string{binding.Key: postgresRecordFor(t, "old")},
			map[string]string{binding.Key: postgresRecordFor(t, "rotated")},
		)

		served := func() string {
			out := &sink{}
			l := New(fetcher, []string{binding.Key}, []Binding{binding}, nil)
			l.Attach(out)
			if err := l.Join(l.Prefetch(context.Background())); err != nil {
				t.Fatalf("join: %v", err)
			}
			msgs := out.messages(t)
			if len(msgs) != 1 {
				t.Fatalf("pushed %d messages, want the cold start's generation", len(msgs))
			}
			return msgs[0].Values[binding.Key]
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
		binding := postgresBinding()
		for name, tc := range map[string]struct {
			values map[string]string
			names  []string
		}{
			"a record published under another type": {
				values: map[string]string{binding.Key: bucketRecord(t, "shop-uploads")},
				names:  []string{"db--main", "BINDING_TYPE_BUCKET", "BINDING_TYPE_POSTGRES", binding.Key},
			},
			"a record carrying no properties at all": {
				values: map[string]string{binding.Key: record(t, &bindingsv1.Binding{Name: "db--main"})},
				names:  []string{"db--main", "BINDING_TYPE_POSTGRES", binding.Key},
			},
			"no record at all": {
				values: map[string]string{"DB_PASSWORD": "hunter2"},
				names:  []string{"db--main", binding.Key, "BINDING_TYPE_POSTGRES"},
			},
			"a value that is not a record": {
				values: map[string]string{binding.Key: "postgres://ocel@db.host:5432/ocel"},
				names:  []string{"db--main", binding.Key},
			},
		} {
			t.Run(name, func(t *testing.T) {
				out := &sink{}
				l := New(resolves(tc.values), []string{binding.Key}, []Binding{binding}, nil)
				l.Attach(out)

				err := l.Join(l.Prefetch(context.Background()))
				if err == nil {
					t.Fatal("join = nil, want a cold start refused rather than an app handed a shape it cannot read")
				}
				if !errors.Is(err, ErrDrift) {
					t.Errorf("error = %v, want it named as drift", err)
				}
				for _, want := range tc.names {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %v, want it to name %q", err, want)
					}
				}
				if msgs := out.messages(t); len(msgs) != 0 {
					t.Errorf("pushed %+v, want nothing delivered to an app whose bindings drifted", msgs)
				}
			})
		}
	})

	t.Run("drift found on a refresh keeps the last good generation serving", func(t *testing.T) {
		binding := postgresBinding()
		clock := time.Unix(1_700_000_000, 0)
		good := postgresRecordFor(t, "good")
		fetcher := resolves(
			map[string]string{binding.Key: good},
			map[string]string{binding.Key: bucketRecord(t, "shop-uploads")},
		)
		out := &sink{}
		l := New(fetcher, []string{binding.Key}, []Binding{binding}, func() time.Time { return clock })
		l.Attach(out)

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound)
		l.Refresh(context.Background())
		eventually(t, "the drifting refresh to run", func() bool { return fetcher.count() == 2 })

		consistently(t, "a warm process pushed a generation it could not conform", func() bool { return len(out.messages(t)) == 1 })
		if msgs := out.messages(t); msgs[0].Values[binding.Key] != good {
			t.Errorf("serving %+v, want the last generation that conformed", msgs[0])
		}
	})
}
