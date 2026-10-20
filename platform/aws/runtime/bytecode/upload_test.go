package bytecode

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type uploadCall struct {
	cache *Cache
	flush Flush
	by    time.Time
}

func (u *uploadCall) run(ctx context.Context) Outcome {
	return u.cache.Upload(ctx, u.by, u.flush)
}

func uploadFixture(store objectStore, ack Flushed, ackOK bool) (*uploadCall, *int) {
	flushes := 0
	u := &uploadCall{
		cache: &Cache{
			store:  store,
			bucket: "assets-xyz",
			key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
			root:   ack.Dir,
		},
		by: time.Now().Add(time.Minute),
		flush: func(context.Context) (Flushed, bool) {
			flushes++
			return ack, ackOK
		},
	}
	return u, &flushes
}

func TestBytecodeUpload(t *testing.T) {
	t.Run("archive keeps the subdirectory node reports", func(t *testing.T) {
		root := t.TempDir()
		nodeDir := filepath.Join(root, "v24.3.1-arm64-9ac5647c-993")
		if err := os.MkdirAll(nodeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "cached.blob"), []byte("compiled bytes"), 0o644); err != nil {
			t.Fatal(err)
		}

		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: nodeDir, OK: true}, true)
		u.cache.root = root

		u.run(context.Background())

		if len(store.puts) != 1 {
			t.Fatalf("puts = %d, want 1", len(store.puts))
		}
		got := readArchive(t, store.puts[0].body)
		const want = "v24.3.1-arm64-9ac5647c-993/cached.blob"
		if got[want] != "compiled bytes" {
			t.Errorf("uploaded archive = %v, want an entry at %s", got, want)
		}
	})

	t.Run("puts to the given bucket and key", func(t *testing.T) {
		dir := cacheDirWith(t, "compiled bytes")
		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: dir, OK: true}, true)

		u.run(context.Background())

		if len(store.heads) != 1 || store.heads[0] != "assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
			t.Fatalf("heads = %v, want a single head of the given key", store.heads)
		}
		if len(store.puts) != 1 {
			t.Fatalf("puts = %d, want 1", len(store.puts))
		}
		put := store.puts[0]
		if put.bucket != "assets-xyz" || put.key != "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
			t.Errorf("put to %s/%s, want assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz", put.bucket, put.key)
		}
		if got := readArchive(t, put.body); got["cached.blob"] != "compiled bytes" {
			t.Errorf("uploaded archive = %v, want it to contain the cache directory's contents", got)
		}
	})

	t.Run("skips the put when the object already exists", func(t *testing.T) {
		dir := cacheDirWith(t, "compiled bytes")
		store := &fakeStore{exists: true}
		u, flushes := uploadFixture(store, Flushed{Dir: dir, OK: true}, true)

		u.run(context.Background())

		if len(store.heads) != 1 {
			t.Errorf("heads = %v, want the key to have been checked", store.heads)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want none once the object exists", store.puts)
		}
		if *flushes != 0 {
			t.Errorf("flushes = %d, want node never asked to flush once the object exists", *flushes)
		}
	})

	t.Run("skips when the flush ack is not OK", func(t *testing.T) {
		cases := []struct {
			name  string
			ack   Flushed
			ackOK bool
		}{
			{name: "node answered not-ok", ack: Flushed{OK: false}, ackOK: true},
			{name: "node never answered", ackOK: false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := &fakeStore{}
				u, _ := uploadFixture(store, tc.ack, tc.ackOK)

				u.run(context.Background())

				if len(store.heads) != 1 {
					t.Errorf("heads = %v, want the key checked before the flush was asked for", store.heads)
				}
				if len(store.puts) != 0 {
					t.Errorf("puts = %v, want nothing without a usable flush", store.puts)
				}
			})
		}
	})

	t.Run("skips entirely when the budget is non positive", func(t *testing.T) {
		store := &fakeStore{}
		u, flushes := uploadFixture(store, Flushed{Dir: t.TempDir(), OK: true}, true)

		u.by = time.Now()
		u.run(context.Background())

		if *flushes != 0 {
			t.Errorf("flushes = %d, want the child left alone", *flushes)
		}
		if len(store.heads) != 0 || len(store.puts) != 0 {
			t.Errorf("touched S3 (heads=%v puts=%v), want nothing", store.heads, store.puts)
		}
	})

	t.Run("skips an empty cache directory", func(t *testing.T) {
		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: t.TempDir(), OK: true}, true)

		u.run(context.Background())

		if len(store.heads) != 1 {
			t.Errorf("heads = %v, want the key checked before the empty cache was found", store.heads)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want nothing to upload", store.puts)
		}
	})

	t.Run("skips when the cache is over the ceiling", func(t *testing.T) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "big.blob"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(CacheCeiling + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()

		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: dir, OK: true}, true)

		start := time.Now()
		u.run(context.Background())

		if len(store.heads) != 1 {
			t.Errorf("heads = %v, want the key checked before the ceiling was decided", store.heads)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want nothing over the ceiling", store.puts)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s, want the ceiling decided before anything was read or compressed", elapsed)
		}
	})

	t.Run("swallows store errors", func(t *testing.T) {
		cases := []struct {
			name  string
			store *fakeStore
		}{
			{name: "head fails", store: &fakeStore{headErr: errors.New("access denied")}},
			{name: "put fails", store: &fakeStore{putErr: errors.New("slow down")}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u, _ := uploadFixture(tc.store, Flushed{Dir: cacheDirWith(t, "x"), OK: true}, true)
				u.run(context.Background())
			})
		}
	})

	t.Run("bounds both store calls by the budget", func(t *testing.T) {
		for _, blockOn := range []string{"head", "put"} {
			t.Run(blockOn, func(t *testing.T) {
				ctx, spendBudget := context.WithCancel(context.Background())
				defer spendBudget()

				store := &blockingStore{blockOn: blockOn, spendBudget: spendBudget}
				u, _ := uploadFixture(store, Flushed{Dir: cacheDirWith(t, "x"), OK: true}, true)

				done := make(chan struct{})
				go func() { defer close(done); u.run(ctx) }()

				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatalf("run never returned, so the %s outlived the budget", blockOn)
				}
				if !store.reached {
					t.Errorf("the %s was never reached, so the test proves nothing", blockOn)
				}
			})
		}
	})

	t.Run("abandons before measuring the cache when the budget is already spent", func(t *testing.T) {
		dir := cacheDirWith(t, "compiled bytes")
		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: dir, OK: true}, true)

		u.flush = func(ctx context.Context) (Flushed, bool) {
			<-ctx.Done()
			return Flushed{Dir: dir, OK: true}, true
		}

		u.by = time.Now().Add(100 * time.Millisecond)

		start := time.Now()
		u.run(context.Background())
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s, want the attempt abandoned at the budget", elapsed)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want none once the budget is spent", store.puts)
		}
	})

	t.Run("abandons an archive build in flight", func(t *testing.T) {
		dir, baseline := bigCacheDir(t)
		store := &fakeStore{}
		u, _ := uploadFixture(store, Flushed{Dir: dir, OK: true}, true)

		u.by = time.Now().Add(baseline / 5)

		start := time.Now()
		u.run(context.Background())
		elapsed := time.Since(start)

		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want nothing once the build was abandoned", store.puts)
		}
		if elapsed > baseline {
			t.Errorf("took %s against a %s full build, want the attempt abandoned partway", elapsed, baseline)
		}
	})

	t.Run("is rooted at the dir compile cache env declares", func(t *testing.T) {
		compileCacheOn(t)
		env := Env()
		if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
			t.Fatalf("Env() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
		}
		dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")

		compileCacheConfigured(t, "proj/prod/web/r1a2b3c4d", "assets-xyz")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")
		t.Setenv("AWS_REGION", "us-east-1")
		c := resolve(context.Background(), func(context.Context) (string, error) { return "v24.3.1", nil })
		if c == nil {
			t.Fatal("resolve() = nil, want a cache for a fully configured function")
		}
		if c.root != dir {
			t.Errorf("cache root = %q, want %q: the two legs disagree on where the cache lives", c.root, dir)
		}
	})
}

type blockingStore struct {
	blockOn     string
	spendBudget context.CancelFunc
	reached     bool
}

func (b *blockingStore) block(ctx context.Context) error {
	b.reached = true
	b.spendBudget()
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingStore) objectExists(ctx context.Context, _, _ string) (bool, error) {
	if b.blockOn != "head" {
		return false, nil
	}
	return false, b.block(ctx)
}

func (b *blockingStore) putObject(ctx context.Context, _, _ string, _ []byte) error {
	if b.blockOn != "put" {
		return nil
	}
	return b.block(ctx)
}

func (b *blockingStore) getObject(context.Context, string, string) (io.ReadCloser, int64, error) {
	return nil, 0, errCacheMiss
}
