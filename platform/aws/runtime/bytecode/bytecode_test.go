package bytecode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/constants"
)

func TestBytecodeCacheKey(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		functionName string
		nodeVersion  string
		goArch       string
		want         string
	}{
		{
			name:         "amd64 maps to the AWS x86_64 spelling",
			prefix:       "prod/proj/web/r1a2b3c4d/bytecode",
			functionName: "my-app",
			nodeVersion:  "24.3.1",
			goArch:       "amd64",
			want:         "prod/proj/web/r1a2b3c4d/bytecode/my-app/node24.3.1-x86_64.tar.gz",
		},
		{
			name:         "arm64 passes through unchanged",
			prefix:       "prod/proj/web/r1a2b3c4d/bytecode",
			functionName: "my-app",
			nodeVersion:  "24.3.1",
			goArch:       "arm64",
			want:         "prod/proj/web/r1a2b3c4d/bytecode/my-app/node24.3.1-arm64.tar.gz",
		},
		{
			name:         "an unrecognized arch still passes through",
			prefix:       "stg/deploy/api/r9f8e7d6c/bytecode",
			functionName: "other-fn",
			nodeVersion:  "20.11.0",
			goArch:       "riscv64",
			want:         "stg/deploy/api/r9f8e7d6c/bytecode/other-fn/node20.11.0-riscv64.tar.gz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheKey(tc.prefix, tc.functionName, tc.nodeVersion, tc.goArch)
			if got != tc.want {
				t.Errorf("cacheKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3Arch(t *testing.T) {
	cases := []struct {
		goArch string
		want   string
	}{
		{"amd64", "x86_64"},
		{"arm64", "arm64"},
		{"386", "386"},
	}
	for _, tc := range cases {
		if got := s3Arch(tc.goArch); got != tc.want {
			t.Errorf("s3Arch(%q) = %q, want %q", tc.goArch, got, tc.want)
		}
	}
}

func TestCanonicalNodeVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{name: "v-prefixed semver", version: "v24.3.1", want: "24.3.1"},
		{name: "no v prefix", version: "20.11.0", want: "20.11.0"},
		{name: "double digit major", version: "v18.19.1", want: "18.19.1"},
		{name: "empty string", version: "", wantErr: true},
		{name: "major only", version: "24", wantErr: true},
		{name: "major and minor only", version: "24.3", wantErr: true},
		{name: "trailing prerelease tag", version: "v24.3.1-nightly", wantErr: true},
		{name: "not a version at all", version: "not-a-version", wantErr: true},
		{name: "whitespace only", version: "   ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalNodeVersion(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("canonicalNodeVersion(%q) = %q, nil, want an error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalNodeVersion(%q) unexpected error: %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("canonicalNodeVersion(%q) = %q, want %q", tc.version, got, tc.want)
			}
			if strings.HasPrefix(got, "v") {
				t.Errorf("canonicalNodeVersion(%q) = %q, want no leading v", tc.version, got)
			}
		})
	}
}

func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(content)
	}
	return out
}

func TestBuildBytecodeArchive(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		dir := t.TempDir()
		versionDir := filepath.Join(dir, "abc123-hash")
		if err := os.MkdirAll(versionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			filepath.Join(dir, "top-level.bin"):       "top level contents",
			filepath.Join(versionDir, "nested-a.bin"): "nested a contents",
			filepath.Join(versionDir, "nested-b.bin"): "nested b contents",
		}
		for path, contents := range files {
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		archive, err := buildArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}

		got := readArchive(t, archive)
		want := map[string]string{
			"top-level.bin":            "top level contents",
			"abc123-hash/nested-a.bin": "nested a contents",
			"abc123-hash/nested-b.bin": "nested b contents",
		}
		if len(got) != len(want) {
			t.Fatalf("archive has %d entries, want %d: %v", len(got), len(want), got)
		}
		for name, contents := range want {
			if got[name] != contents {
				t.Errorf("archive[%q] = %q, want %q", name, got[name], contents)
			}
		}
	})

	t.Run("skips non regular files", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.bin")
		if err := os.WriteFile(target, []byte("real contents"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "link.bin")); err != nil {
			t.Skipf("symlinks unsupported in this environment: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}

		archive, err := buildArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}
		got := readArchive(t, archive)
		if _, ok := got["link.bin"]; ok {
			t.Errorf("archive contains symlink link.bin, want it skipped")
		}
		if _, ok := got["subdir"]; ok {
			t.Errorf("archive contains directory entry subdir, want it skipped")
		}
		if got["real.bin"] != "real contents" {
			t.Errorf("archive[real.bin] = %q, want %q", got["real.bin"], "real contents")
		}
	})

	t.Run("missing directory is empty not an error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")

		archive, err := buildArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}
		got := readArchive(t, archive)
		if len(got) != 0 {
			t.Errorf("archive has %d entries, want 0: %v", len(got), got)
		}
	})

	t.Run("stops the walk when the context ends", func(t *testing.T) {
		dir, baseline := bigCacheDir(t)

		ctx, cancel := context.WithTimeout(context.Background(), baseline/5)
		defer cancel()

		start := time.Now()
		_, err := buildArchive(ctx, dir)
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("buildArchive() error = %v, want the context's", err)
		}
		if elapsed > baseline/2 {
			t.Errorf("took %s against a %s full build, want the walk stopped partway", elapsed, baseline)
		}
	})
}

func TestExceedsBytecodeCacheCeiling(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want bool
	}{
		{name: "well under the ceiling", size: 1024, want: false},
		{name: "exactly at the ceiling", size: CacheCeiling, want: false},
		{name: "one byte over the ceiling", size: CacheCeiling + 1, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exceedsCeiling(tc.size); got != tc.want {
				t.Errorf("exceedsCeiling(%d) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}
}

type fakeStore struct {
	mu      sync.Mutex
	exists  bool
	headErr error
	putErr  error
	heads   []string
	puts    []fakePut

	getBody io.ReadCloser
	getSize int64
	getErr  error
	gets    []string
}

type fakePut struct {
	bucket string
	key    string
	body   []byte
}

func (f *fakeStore) objectExists(_ context.Context, bucket, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, bucket+"/"+key)
	return f.exists, f.headErr
}

func (f *fakeStore) putObject(_ context.Context, bucket, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, fakePut{bucket: bucket, key: key, body: body})
	return f.putErr
}

func (f *fakeStore) getObject(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, bucket+"/"+key)
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	return f.getBody, f.getSize, nil
}

func cacheDirWith(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cached.blob"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

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
			t.Errorf("uploaded archive = %v, want it to carry the cache directory's contents", got)
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
		t.Setenv(prefixEnvVar, "ocel")
		env := Env()
		if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
			t.Fatalf("Env() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
		}
		dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")

		t.Setenv(bucketEnvVar, "assets-xyz")
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

func bigCacheDir(t *testing.T) (string, time.Duration) {
	t.Helper()
	dir := t.TempDir()
	filler := rand.NewChaCha8([32]byte{})
	buf := make([]byte, 8<<10)
	for i := range 400 {
		if _, err := filler.Read(buf); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.blob", i)), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	if _, err := buildArchive(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	baseline := time.Since(start)
	if baseline < 10*time.Millisecond {
		t.Skipf("archiving is too fast here (%s) to cancel partway reliably", baseline)
	}
	return dir, baseline
}

func TestBuildArchiveWithin(t *testing.T) {
	t.Run("abandons on an expired context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := buildArchiveWithin(ctx, cacheDirWith(t, "compiled bytes"))
		if err == nil {
			t.Fatal("buildArchiveWithin() error = nil, want the budget reported")
		}
		if !strings.Contains(err.Error(), "no budget left") {
			t.Errorf("error = %q, want the pre-check's, since the budget was spent before the call", err)
		}
	})

	t.Run("abandons a build already in flight", func(t *testing.T) {
		dir, baseline := bigCacheDir(t)

		ctx, cancel := context.WithTimeout(context.Background(), baseline/5)
		defer cancel()

		start := time.Now()
		_, err := buildArchiveWithin(ctx, dir)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("buildArchiveWithin() error = nil, want the build abandoned")
		}
		if !strings.Contains(err.Error(), "outlasted the upload budget") {
			t.Errorf("error = %q, want the select's, since the budget was live on entry", err)
		}
		if elapsed > baseline/2 {
			t.Errorf("took %s against a %s full build, want the caller released partway", elapsed, baseline)
		}
	})

	t.Run("returns the archive within budget", func(t *testing.T) {
		archive, err := buildArchiveWithin(context.Background(), cacheDirWith(t, "compiled bytes"))
		if err != nil {
			t.Fatalf("buildArchiveWithin: %v", err)
		}
		if got := readArchive(t, archive); got["cached.blob"] != "compiled bytes" {
			t.Errorf("archive = %v, want the cache directory's contents", got)
		}
	})
}

func TestCompileCacheSize(t *testing.T) {
	t.Run("charges tarEntryOverhead per file and reads a missing directory as empty", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.blob"), []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested", "b.blob"), []byte("678"), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := compileCacheSize(context.Background(), dir)
		if err != nil {
			t.Fatalf("compileCacheSize: %v", err)
		}
		if want := int64(8 + 2*tarEntryOverhead); got != want {
			t.Errorf("compileCacheSize() = %d, want %d (payload plus tarEntryOverhead per file, matching untarInto's charge)", got, want)
		}

		missing, err := compileCacheSize(context.Background(), filepath.Join(dir, "does-not-exist"))
		if err != nil {
			t.Fatalf("compileCacheSize on a missing dir: %v", err)
		}
		if missing != 0 {
			t.Errorf("compileCacheSize(missing) = %d, want 0", missing)
		}
	})

	t.Run("stops on an expired context", func(t *testing.T) {
		dir := cacheDirWith(t, "compiled bytes")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := compileCacheSize(ctx, dir); !errors.Is(err, context.Canceled) {
			t.Errorf("compileCacheSize() error = %v, want the context's", err)
		}
	})
}

func TestCompileCacheEnv(t *testing.T) {
	t.Run("gate closed", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "")
		if got := Env(); got != nil {
			t.Errorf("Env() = %v, want nil with no prefix configured", got)
		}
	})
	t.Run("gate open", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel")
		want := []string{"NODE_COMPILE_CACHE=" + compileCacheDir}
		got := Env()
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("Env() = %v, want %v", got, want)
		}
	})
}

func TestResolveBytecodeResolution(t *testing.T) {
	t.Run("nil when not fully configured", func(t *testing.T) {
		cases := []struct {
			name     string
			prefix   string
			bucket   string
			function string
		}{
			{name: "no prefix", bucket: "assets", function: "my-app"},
			{name: "no bucket", prefix: "ocel", function: "my-app"},
			{name: "no function name", prefix: "ocel", bucket: "assets"},
		}
		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv(prefixEnvVar, tc.prefix)
				t.Setenv(bucketEnvVar, tc.bucket)
				t.Setenv("AWS_LAMBDA_FUNCTION_NAME", tc.function)
				if got := resolve(context.Background(), nodeVersion); got != nil {
					t.Errorf("resolve() = %+v, want nil", got)
				}
			})
		}
	})

	t.Run("nil when the node version cannot be read", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel")
		t.Setenv(bucketEnvVar, "assets")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")

		cases := []struct {
			name    string
			version string
			err     error
		}{
			{name: "the binary would not run", err: errors.New("exec format error")},
			{name: "the output is not a version", version: "not-a-version"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				nodeVersion := func(context.Context) (string, error) { return tc.version, tc.err }
				if got := resolve(context.Background(), nodeVersion); got != nil {
					t.Errorf("resolve() = %+v, want nil", got)
				}
			})
		}
	})

	t.Run("carries the environment and version into the key", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "stg/proj/web/r1a2b3c4d/bytecode")
		t.Setenv(bucketEnvVar, "assets-xyz")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolve(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolve() = nil, want a resolution for a fully configured function")
		}
		if r.bucket != "assets-xyz" {
			t.Errorf("bucket = %q, want %q", r.bucket, "assets-xyz")
		}
		if r.store == nil {
			t.Error("store = nil, want an S3-backed store")
		}
		want := "stg/proj/web/r1a2b3c4d/bytecode/my-app/node24.3.1-" + s3Arch(runtime.GOARCH) + ".tar.gz"
		if r.key != want {
			t.Errorf("key = %q, want %q", r.key, want)
		}
	})

	t.Run("resolves for a function with no isr bucket", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel/stg")
		t.Setenv(bucketEnvVar, "assets-xyz")
		t.Setenv("OCEL_ISR_BUCKET", "")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-express-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolve(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolve() = nil, want a resolution for a node runtime function")
		}
		if r.bucket != "assets-xyz" {
			t.Errorf("bucket = %q, want %q", r.bucket, "assets-xyz")
		}
	})
}

func TestBudgetUntil(t *testing.T) {
	t.Run("no deadline yields the cap", func(t *testing.T) {
		if got := budgetUntil(time.Time{}); got != UploadBudget {
			t.Errorf("budgetUntil() = %s, want %s", got, UploadBudget)
		}
	})
	t.Run("a distant deadline is capped", func(t *testing.T) {
		if got := budgetUntil(time.Now().Add(time.Minute)); got != UploadBudget {
			t.Errorf("budgetUntil() = %s, want %s", got, UploadBudget)
		}
	})
	t.Run("a near deadline yields what is left", func(t *testing.T) {
		got := budgetUntil(time.Now().Add(time.Second))
		if got <= 0 || got > time.Second {
			t.Errorf("budgetUntil() = %s, want a positive budget under 1s", got)
		}
	})
	t.Run("a deadline already passed yields nothing", func(t *testing.T) {
		if got := budgetUntil(time.Now().Add(-time.Millisecond)); got > 0 {
			t.Errorf("budgetUntil() = %s, want a non-positive budget", got)
		}
	})
}

func newS3Store(t *testing.T, handler http.HandlerFunc) (objectStore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     &srv.URL,
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		RetryMaxAttempts: 1,
	})
	return s3Store{client: client}, srv
}

func TestS3BytecodeStore(t *testing.T) {
	t.Run("object exists", func(t *testing.T) {
		cases := []struct {
			name       string
			status     int
			wantExists bool
			wantErr    bool
		}{
			{name: "404 means the object is absent", status: http.StatusNotFound, wantExists: false},
			{name: "200 means it is already there", status: http.StatusOK, wantExists: true},
			{name: "403 is how an absent key reads without s3:ListBucket", status: http.StatusForbidden, wantExists: false},
			{name: "500 is a real error", status: http.StatusInternalServerError, wantErr: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodHead {
						t.Errorf("method = %s, want HEAD", r.Method)
					}
					w.WriteHeader(tc.status)
				})

				exists, err := store.objectExists(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz")
				if tc.wantErr {
					if err == nil {
						t.Fatalf("objectExists() = %v, nil, want an error", exists)
					}
					return
				}
				if err != nil {
					t.Fatalf("objectExists() unexpected error: %v", err)
				}
				if exists != tc.wantExists {
					t.Errorf("objectExists() = %v, want %v", exists, tc.wantExists)
				}
			})
		}
	})

	t.Run("put object", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})

		if err := store.putObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz", []byte("archive bytes")); err != nil {
			t.Fatalf("putObject: %v", err)
		}
		if want := "/assets-xyz/ocel/bytecode/my-app/node24-arm64.tar.gz"; gotPath != want {
			t.Errorf("put path = %q, want %q", gotPath, want)
		}
		if string(gotBody) != "archive bytes" {
			t.Errorf("put body = %q, want %q", gotBody, "archive bytes")
		}
	})

	t.Run("put object reports a failure", func(t *testing.T) {
		store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

		if err := store.putObject(context.Background(), "assets-xyz", "some/key", []byte("x")); err == nil {
			t.Error("putObject() error = nil, want the failure reported")
		}
	})

	t.Run("get object", func(t *testing.T) {
		t.Run("200 returns the body and content length", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				w.Header().Set("Content-Length", "13")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("archive bytes"))
			})

			body, size, err := store.getObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz")
			if err != nil {
				t.Fatalf("getObject: %v", err)
			}
			defer body.Close()
			if size != 13 {
				t.Errorf("size = %d, want 13", size)
			}
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != "archive bytes" {
				t.Errorf("body = %q, want %q", got, "archive bytes")
			}
		})

		t.Run("a NoSuchKey error body is a miss, not an error", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "missing/key")
			if !errors.Is(err, errCacheMiss) {
				t.Errorf("getObject() error = %v, want errCacheMiss", err)
			}
		})

		t.Run("403 is a real error, not an absence", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "some/key")
			if err == nil {
				t.Fatal("getObject() error = nil, want the failure reported")
			}
			if errors.Is(err, errCacheMiss) {
				t.Error("getObject() reported a miss for an access failure")
			}
		})
	})
}

type tarEntry struct {
	name     string
	typeflag byte
	content  []byte
	linkname string
}

func tarBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.content)),
			Mode:     0o644,
			Linkname: e.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header for %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content for %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func ustarHeader(name string, typeflag byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b[0:100], name)
	copy(b[100:108], fmt.Appendf(nil, "%07o\x00", 0o644))
	copy(b[108:116], fmt.Appendf(nil, "%07o\x00", 0))
	copy(b[116:124], fmt.Appendf(nil, "%07o\x00", 0))
	copy(b[124:136], fmt.Appendf(nil, "%011o\x00", size))
	copy(b[136:148], fmt.Appendf(nil, "%011o\x00", 0))
	for i := 148; i < 156; i++ {
		b[i] = ' '
	}
	b[156] = typeflag
	copy(b[257:263], "ustar\x00")
	copy(b[263:265], "00")

	var sum int
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Appendf(nil, "%06o\x00 ", sum))
	return b
}

func ustarArchive(t *testing.T, name string, typeflag byte, content []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	raw.Write(ustarHeader(name, typeflag, int64(len(content))))
	raw.Write(content)
	if pad := (512 - len(content)%512) % 512; pad > 0 {
		raw.Write(make([]byte, pad))
	}
	raw.Write(make([]byte, 1024))

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestUntarInto(t *testing.T) {
	t.Run("rejects an entry that resolves to the cache directory itself", func(t *testing.T) {
		for _, name := range []string{"", ".", "./"} {
			t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
				var archive []byte
				if name == "./" {
					archive = ustarArchive(t, name, tar.TypeReg, []byte("abc"))
				} else {
					archive = tarBytes(t, []tarEntry{{name: name, typeflag: tar.TypeReg, content: []byte("abc")}})
				}

				dest := filepath.Join(t.TempDir(), "cache")
				if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, CacheCeiling); err == nil {
					t.Fatalf("untarGzipInto(name=%q) error = nil, want the entry rejected", name)
				}
				if info, err := os.Stat(dest); err == nil {
					t.Fatalf("dest = %v after a rejected entry, want it never created", info.Mode())
				}
			})
		}
	})

	t.Run("round trip", func(t *testing.T) {
		src := t.TempDir()
		versionDir := filepath.Join(src, "v24.3.1-x64-abc123-1000")
		if err := os.MkdirAll(versionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			filepath.Join(src, "index.bin"):       "top level",
			filepath.Join(versionDir, "aabbccdd"): "nested one",
			filepath.Join(versionDir, "11223344"): "nested two",
		}
		for path, contents := range files {
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		archive, err := buildArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}

		dest := t.TempDir()
		n, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, CacheCeiling)
		if err != nil {
			t.Fatalf("untarGzipInto: %v", err)
		}
		var want int64
		for _, contents := range files {
			want += int64(len(contents)) + tarEntryOverhead
		}
		if n != want {
			t.Errorf("untarGzipInto() = %d bytes, want %d", n, want)
		}
		for path, contents := range files {
			rel, err := filepath.Rel(src, path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dest, rel))
			if err != nil {
				t.Fatalf("read back %s: %v", rel, err)
			}
			if string(got) != contents {
				t.Errorf("%s = %q, want %q", rel, got, contents)
			}
		}
	})

	t.Run("clamps extracted file permissions", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		content := []byte("compiled")
		if err := tw.WriteHeader(&tar.Header{
			Name:     "unreadable.blob",
			Typeflag: tar.TypeReg,
			Size:     int64(len(content)),
			Mode:     0o000,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("write content: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("close tar: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("close gzip: %v", err)
		}

		dest := t.TempDir()
		if _, err := untarGzipInto(context.Background(), bytes.NewReader(buf.Bytes()), dest, CacheCeiling); err != nil {
			t.Fatalf("untarGzipInto: %v", err)
		}

		info, err := os.Stat(filepath.Join(dest, "unreadable.blob"))
		if err != nil {
			t.Fatalf("stat extracted file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("extracted file mode = %o, want 0644 regardless of the archive's declared mode", got)
		}
	})

	t.Run("aborts past the ceiling", func(t *testing.T) {
		archive := tarBytes(t, []tarEntry{
			{name: "a.bin", typeflag: tar.TypeReg, content: bytes.Repeat([]byte("x"), 100)},
			{name: "b.bin", typeflag: tar.TypeReg, content: bytes.Repeat([]byte("y"), 100)},
		})

		if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), t.TempDir(), 150); err == nil {
			t.Fatal("untarGzipInto() error = nil, want the ceiling enforced")
		}
	})

	t.Run("bounds entry count even with zero sized entries", func(t *testing.T) {
		entries := make([]tarEntry, 1000)
		for i := range entries {
			entries[i] = tarEntry{name: fmt.Sprintf("%04d.bin", i), typeflag: tar.TypeReg}
		}
		archive := tarBytes(t, entries)

		const ceilingEntries = 10
		ceiling := int64(ceilingEntries * tarEntryOverhead)
		dest := t.TempDir()

		if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, ceiling); err == nil {
			t.Fatal("untarGzipInto() error = nil, want the ceiling enforced against entry count alone")
		}

		created, err := os.ReadDir(dest)
		if err != nil {
			t.Fatalf("read dest: %v", err)
		}
		if len(created) != ceilingEntries {
			t.Errorf("created %d entries under a %d-entry ceiling, want exactly %d", len(created), ceilingEntries, ceilingEntries)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func rehydrateFixture(archive []byte) *fakeStore {
	return &fakeStore{getBody: io.NopCloser(bytes.NewReader(archive)), getSize: int64(len(archive))}
}

func assertNoCacheDir(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cache dir %s exists after a failed rehydration, want it absent", dir)
	}
}

func TestRehydrateCompileCache(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		src := t.TempDir()
		versionDir := filepath.Join(src, "v24.3.1-x64-abc123-1000")
		if err := os.MkdirAll(versionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "index.bin"), []byte("top"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(versionDir, "aabbccdd"), []byte("nested"), 0o644); err != nil {
			t.Fatal(err)
		}
		archive, err := buildArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}
		store := rehydrateFixture(archive)
		dest := filepath.Join(t.TempDir(), "cache")

		n, ok := rehydrateCompileCache(context.Background(), store, "assets-xyz", "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz", dest)
		if !ok {
			t.Fatal("rehydrateCompileCache() ok = false, want a successful round trip")
		}
		if want := int64(len("top")+len("nested")) + 2*tarEntryOverhead; n != want {
			t.Errorf("rehydrateCompileCache() = %d bytes, want %d", n, want)
		}
		if got, err := os.ReadFile(filepath.Join(dest, "index.bin")); err != nil || string(got) != "top" {
			t.Errorf("index.bin = %q, %v, want %q, nil", got, err, "top")
		}
		if got, err := os.ReadFile(filepath.Join(dest, "v24.3.1-x64-abc123-1000", "aabbccdd")); err != nil || string(got) != "nested" {
			t.Errorf("nested file = %q, %v, want %q, nil", got, err, "nested")
		}
		if len(store.gets) != 1 || store.gets[0] != "assets-xyz/ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
			t.Errorf("gets = %v, want a single get of the composed key", store.gets)
		}
	})

	t.Run("miss touches nothing", func(t *testing.T) {
		store := &fakeStore{getErr: errCacheMiss}
		dest := filepath.Join(t.TempDir(), "cache")

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) on a miss", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("logs a miss differently from a failure", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")

		missOut := captureStderr(t, func() {
			store := &fakeStore{getErr: errCacheMiss}
			rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		})
		if !strings.Contains(missOut, "nothing to rehydrate") {
			t.Errorf("miss log = %q, want it to name a miss", missOut)
		}
		if strings.Contains(missOut, "could not") {
			t.Errorf("miss log = %q, want no failure wording for the expected first-cold-start case", missOut)
		}

		failOut := captureStderr(t, func() {
			store := &fakeStore{getErr: errors.New("access denied")}
			rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		})
		if !strings.Contains(failOut, "could not fetch") {
			t.Errorf("failure log = %q, want it to name a fetch failure", failOut)
		}
		if strings.Contains(failOut, "nothing to rehydrate") {
			t.Errorf("failure log = %q, want it not to read as the expected miss case", failOut)
		}

		if missOut == failOut {
			t.Error("miss and failure produced the identical log line")
		}
	})

	t.Run("non gzip body leaves no directory", func(t *testing.T) {
		body := []byte("not a gzip stream")
		store := &fakeStore{getBody: io.NopCloser(bytes.NewReader(body)), getSize: int64(len(body))}
		dest := filepath.Join(t.TempDir(), "cache")

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) on a corrupt body", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("rejects traversal", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "cache")
		archive := tarBytes(t, []tarEntry{{name: "../escape", typeflag: tar.TypeReg, content: []byte("hax")}})
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for a traversal entry", n, ok)
		}
		assertNoCacheDir(t, dest)
		if _, err := os.Stat(filepath.Join(root, "escape")); !errors.Is(err, fs.ErrNotExist) {
			t.Error("a traversal entry escaped into the temp dir's parent")
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		archive := tarBytes(t, []tarEntry{{name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("hax")}})
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for an absolute path", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("rejects symlink", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		archive := tarBytes(t, []tarEntry{{name: "link.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}})
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for a symlink entry", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("rejects an entry that resolves to the cache directory itself", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		archive := ustarArchive(t, "./", tar.TypeReg, []byte("abc"))
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for an entry targeting dir itself", n, ok)
		}
		if info, err := os.Stat(dest); err == nil {
			t.Fatalf("dest = %v, want it absent rather than replaced by a file", info.Mode())
		}
	})

	t.Run("bails on content length before any read", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		store := &fakeStore{
			getBody: io.NopCloser(poisonReader{t}),
			getSize: CacheCeiling + 1,
		}

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) over the ceiling", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("aborts when streamed content exceeds the ceiling", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		size := int64(CacheCeiling) + 1<<20
		if err := tw.WriteHeader(&tar.Header{Name: "huge.bin", Typeflag: tar.TypeReg, Size: size, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tw, zeroReader{}, size); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		archive := buf.Bytes()

		dest := filepath.Join(t.TempDir(), "cache")
		store := rehydrateFixture(archive)
		if store.getSize >= CacheCeiling {
			t.Fatalf("test archive's compressed size %d did not stay under the ceiling", store.getSize)
		}

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) once streamed content passes the ceiling", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("wipes stale content before extracting", func(t *testing.T) {
		dest := t.TempDir()
		stale := filepath.Join(dest, "stale.bin")
		if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}

		archive := tarBytes(t, []tarEntry{{name: "fresh.bin", typeflag: tar.TypeReg, content: []byte("new")}})
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if want := int64(3 + tarEntryOverhead); !ok || n != want {
			t.Fatalf("rehydrateCompileCache() = (%d, %v), want (%d, true)", n, ok, want)
		}
		if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
			t.Error("stale content survived rehydration")
		}
		if got, err := os.ReadFile(filepath.Join(dest, "fresh.bin")); err != nil || string(got) != "new" {
			t.Errorf("fresh.bin = %q, %v, want %q, nil", got, err, "new")
		}
	})

	t.Run("cancelled context aborts and cleans up", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		pr, pw := io.Pipe()
		go func() {
			gz := gzip.NewWriter(pw)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: "a.bin", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
			tw.Write([]byte("abc"))
			gz.Flush()
		}()

		store := &fakeStore{getBody: pr, getSize: 1 << 20}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		n, ok := rehydrateCompileCache(ctx, store, "bucket", "key", dest)
		elapsed := time.Since(start)

		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) once cancelled", n, ok)
		}
		if elapsed > time.Second {
			t.Errorf("took %s, want the extraction interrupted at the context deadline", elapsed)
		}
		assertNoCacheDir(t, dest)
	})
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

type poisonReader struct{ t *testing.T }

func (p poisonReader) Read([]byte) (int, error) {
	p.t.Fatal("read from a body that should have been rejected on ContentLength alone")
	return 0, io.EOF
}

func TestRehydrateBytecodeCache(t *testing.T) {
	t.Run("logs the hit with key bytes and elapsed MS", func(t *testing.T) {
		archive, err := buildArchive(context.Background(), cacheDirWith(t, "compiled bytes"))
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}
		r := &Cache{
			store:  rehydrateFixture(archive),
			bucket: "assets-xyz",
			key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
		}
		dest := filepath.Join(t.TempDir(), "cache")

		var hit bool
		out := captureStderr(t, func() {
			hit = rehydrateInto(context.Background(), r, dest)
		})
		if !hit {
			t.Fatal("rehydrateInto() = false, want a hit")
		}
		if !strings.Contains(out, "ocel: rehydrated compile cache from ocel/bytecode/my-app/node24.3.1-arm64.tar.gz:") {
			t.Errorf("log = %q, want it to name the key", out)
		}
		if !strings.Contains(out, "bytes in") || !strings.Contains(out, "ms") {
			t.Errorf("log = %q, want bytes and elapsed ms", out)
		}
	})

	t.Run("miss logs nothing of its own", func(t *testing.T) {
		r := &Cache{store: &fakeStore{getErr: errCacheMiss}, bucket: "b", key: "k"}
		dest := filepath.Join(t.TempDir(), "cache")

		var hit bool
		out := captureStderr(t, func() {
			hit = rehydrateInto(context.Background(), r, dest)
		})
		if hit {
			t.Fatal("rehydrateInto() = true, want a miss")
		}
		if strings.Count(out, "\n") != 1 {
			t.Errorf("log = %q, want exactly the one line rehydrateCompileCache already wrote", out)
		}
	})

	t.Run("applies its own budget", func(t *testing.T) {
		r := &Cache{store: blockingGet{}, bucket: "b", key: "k"}
		dest := filepath.Join(t.TempDir(), "cache")

		done := make(chan bool, 1)
		go func() { done <- rehydrateInto(context.Background(), r, dest) }()

		select {
		case hit := <-done:
			if hit {
				t.Error("rehydrateInto() = true, want false once the store hangs past its budget")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("rehydrateInto did not return within 3s, want rehydrateBudget enforced")
		}
	})
}

type blockingGet struct{}

func (blockingGet) objectExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (blockingGet) putObject(context.Context, string, string, []byte) error { return nil }
func (blockingGet) getObject(ctx context.Context, _, _ string) (io.ReadCloser, int64, error) {
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func TestBytecodeRehydrate(t *testing.T) {
	t.Run("targets the dir compile cache env declares", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel")
		env := Env()
		if len(env) != 1 || env[0] != "NODE_COMPILE_CACHE="+compileCacheDir {
			t.Fatalf("Env() = %v, want exactly [NODE_COMPILE_CACHE=%s]", env, compileCacheDir)
		}

		dir := filepath.Join(t.TempDir(), "compile-cache")
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "cached.blob"), []byte("compiled"), 0o644); err != nil {
			t.Fatal(err)
		}
		archive, err := buildArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildArchive: %v", err)
		}
		r := &Cache{store: rehydrateFixture(archive), bucket: "b", key: "k"}

		if !rehydrateInto(context.Background(), r, dir) {
			t.Fatal("rehydrateInto() = false, want a hit")
		}
		got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
		if err != nil || string(got) != "compiled" {
			t.Errorf("file at Env's dir = %q, %v, want %q, nil: rehydrateInto wrote somewhere else", got, err, "compiled")
		}
	})
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.content)),
			Mode:     0o644,
			Linkname: e.linkname,
		}); err != nil {
			t.Fatalf("write header for %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content for %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func TestEmbeddedBytecodePath(t *testing.T) {
	t.Run("follows the resolutions key", func(t *testing.T) {
		cases := []struct {
			key  string
			want string
		}{
			{cacheKey("ocel", "my-app", "24.3.1", "arm64"), embeddedDir + "/node24.3.1-arm64.tar"},
			{cacheKey("stg/deploy", "other-fn", "20.11.0", "amd64"), embeddedDir + "/node20.11.0-x86_64.tar"},
		}
		for _, c := range cases {
			if got := embeddedPath(c.key); got != c.want {
				t.Errorf("embeddedPath(%q) = %q, want %q", c.key, got, c.want)
			}
		}
	})

	t.Run("empty for a key that is not a cache tarball", func(t *testing.T) {
		for _, key := range []string{"", "ocel/bytecode/my-app", "ocel/bytecode/my-app/node24.3.1-arm64.tar", "some/other/object.zip"} {
			if got := embeddedPath(key); got != "" {
				t.Errorf("embeddedPath(%q) = %q, want no path at all", key, got)
			}
		}
	})
}

func TestLoadEmbeddedBytecodeCache(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		src := t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, "v24.3.1-x64-abc123-1000"), 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"index.bin":                        "top level",
			"v24.3.1-x64-abc123-1000/aabbccdd": "nested one",
			"v24.3.1-x64-abc123-1000/11223344": "nested two",
		}
		entries := make([]tarEntry, 0, len(files))
		for name, contents := range files {
			entries = append(entries, tarEntry{name: name, typeflag: tar.TypeReg, content: []byte(contents)})
		}
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, entries), 0o644); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(t.TempDir(), "cache")
		n, hit := loadEmbedded(context.Background(), tarPath, dest)
		if !hit {
			t.Fatal("loadEmbedded() = false, want a hit")
		}
		var want int64
		for _, contents := range files {
			want += int64(len(contents)) + tarEntryOverhead
		}
		if n != want {
			t.Errorf("loadEmbedded() = %d bytes, want %d", n, want)
		}
		for name, contents := range files {
			got, err := os.ReadFile(filepath.Join(dest, name))
			if err != nil || string(got) != contents {
				t.Errorf("%s = %q, %v, want %q, nil", name, got, err, contents)
			}
		}
	})

	t.Run("absent tar touches nothing", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "left.bin"), []byte("untouched"), 0o644); err != nil {
			t.Fatal(err)
		}

		var hit bool
		out := captureStderr(t, func() {
			_, hit = loadEmbedded(context.Background(), filepath.Join(t.TempDir(), "nothing-here.tar"), dest)
		})
		if hit {
			t.Fatal("loadEmbedded() = true, want a miss")
		}
		if out != "" {
			t.Errorf("log = %q, want nothing said about an artifact built without the embed pass", out)
		}
		if got, err := os.ReadFile(filepath.Join(dest, "left.bin")); err != nil || string(got) != "untouched" {
			t.Errorf("cache dir = %q, %v, want it left alone", got, err)
		}
	})

	t.Run("corrupt leaves no directory", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, []byte("this is not a tar stream at all"), 0o644); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(t.TempDir(), "cache")
		var hit bool
		out := captureStderr(t, func() {
			_, hit = loadEmbedded(context.Background(), tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbedded() = true, want a corrupt tar reported as a miss")
		}
		if !strings.Contains(out, tarPath) {
			t.Errorf("log = %q, want it to name the embedded tar", out)
		}
		if info, err := os.Stat(dest); err == nil {
			t.Errorf("dest = %v after a corrupt tar, want it wiped", info.Mode())
		}
	})

	t.Run("rejects hostile entries", func(t *testing.T) {
		cases := map[string]tarEntry{
			"traversal":     {name: "../escaped.bin", typeflag: tar.TypeReg, content: []byte("nope")},
			"absolute path": {name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("nope")},
			"symlink":       {name: "link.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		}
		for name, entry := range cases {
			t.Run(name, func(t *testing.T) {
				tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
				if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{entry}), 0o644); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(t.TempDir(), "cache")

				var hit bool
				captureStderr(t, func() {
					_, hit = loadEmbedded(context.Background(), tarPath, dest)
				})
				if hit {
					t.Fatalf("loadEmbedded() = true for a %s entry, want it rejected", name)
				}
				if info, err := os.Stat(dest); err == nil {
					t.Errorf("dest = %v after a rejected entry, want it wiped", info.Mode())
				}
			})
		}
	})

	t.Run("refuses a spent budget", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "blob", typeflag: tar.TypeReg, content: []byte("x")}}), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		dest := filepath.Join(t.TempDir(), "cache")
		var hit bool
		captureStderr(t, func() {
			_, hit = loadEmbedded(ctx, tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbedded() = true, want a spent budget to skip the attempt")
		}
		if info, err := os.Stat(dest); err == nil {
			t.Errorf("dest = %v, want an untouched directory", info.Mode())
		}
	})

	t.Run("stops when the budget runs out mid extraction", func(t *testing.T) {
		entries := make([]tarEntry, 0, 20000)
		for i := range 20000 {
			entries = append(entries, tarEntry{name: fmt.Sprintf("e/%05d.bin", i), typeflag: tar.TypeReg})
		}
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, entries), 0o644); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(t.TempDir(), "cache")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		var hit bool
		out := captureStderr(t, func() {
			_, hit = loadEmbedded(ctx, tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbedded() = true, want the extraction cut off by the shared budget")
		}
		if !strings.Contains(out, tarPath) {
			t.Errorf("log = %q, want it to name the embedded tar", out)
		}
		if info, err := os.Stat(dest); err == nil {
			t.Errorf("dest = %v, want a partial extraction wiped", info.Mode())
		}
	})
}

func TestEmbeddedBytecodeCache(t *testing.T) {
	t.Run("logs a line distinct from the S3 rehydrate", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, constants.ProjectStateDirName, "bytecode")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		tarPath := filepath.Join(dir, "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "cached.blob", typeflag: tar.TypeReg, content: []byte("compiled")}}), 0o644); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(t.TempDir(), "cache")
		var hit bool
		out := captureStderr(t, func() {
			hit = loadEmbeddedCache(context.Background(), tarPath, dest)
		})
		if !hit {
			t.Fatal("loadEmbeddedCache() = false, want a hit")
		}
		if !strings.Contains(out, "ocel: loaded embedded compile cache from "+tarPath+":") {
			t.Errorf("log = %q, want it to name the embedded tar", out)
		}
		if strings.Contains(out, "rehydrated compile cache from") {
			t.Errorf("log = %q, want it distinguishable from the S3 rehydrate line", out)
		}
		if !strings.Contains(out, "bytes in") || !strings.Contains(out, "ms") {
			t.Errorf("log = %q, want bytes and elapsed ms", out)
		}
	})
}

func TestBytecodeEmbedded(t *testing.T) {
	t.Run("targets the dir compile cache env declares", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel")
		env := Env()
		if len(env) != 1 || env[0] != "NODE_COMPILE_CACHE="+compileCacheDir {
			t.Fatalf("Env() = %v, want exactly [NODE_COMPILE_CACHE=%s]", env, compileCacheDir)
		}

		dir := filepath.Join(t.TempDir(), "compile-cache")
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "cached.blob", typeflag: tar.TypeReg, content: []byte("compiled")}}), 0o644); err != nil {
			t.Fatal(err)
		}
		captureStderr(t, func() {
			if !loadEmbeddedCache(context.Background(), tarPath, dir) {
				t.Error("loadEmbeddedCache() = false, want a hit")
			}
		})
		got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
		if err != nil || string(got) != "compiled" {
			t.Errorf("file at Env's dir = %q, %v, want %q, nil", got, err, "compiled")
		}
	})
}
