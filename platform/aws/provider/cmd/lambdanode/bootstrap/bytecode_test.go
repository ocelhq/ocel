package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
			got := bytecodeCacheKey(tc.prefix, tc.functionName, tc.nodeVersion, tc.goArch)
			if got != tc.want {
				t.Errorf("bytecodeCacheKey() = %q, want %q", got, tc.want)
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
		if err == io.EOF {
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

		archive, err := buildBytecodeArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
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

		archive, err := buildBytecodeArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
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

		archive, err := buildBytecodeArchive(context.Background(), dir)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
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
		_, err := buildBytecodeArchive(ctx, dir)
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("buildBytecodeArchive() error = %v, want the context's", err)
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
		{name: "exactly at the ceiling", size: bytecodeCacheCeiling, want: false},
		{name: "one byte over the ceiling", size: bytecodeCacheCeiling + 1, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exceedsBytecodeCacheCeiling(tc.size); got != tc.want {
				t.Errorf("exceedsBytecodeCacheCeiling(%d) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}
}

type fakeBytecodeStore struct {
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

func (f *fakeBytecodeStore) objectExists(_ context.Context, bucket, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, bucket+"/"+key)
	return f.exists, f.headErr
}

func (f *fakeBytecodeStore) putObject(_ context.Context, bucket, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, fakePut{bucket: bucket, key: key, body: body})
	return f.putErr
}

func (f *fakeBytecodeStore) getObject(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
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

func uploadFixture(store bytecodeStore, ack compileCacheFlushedPayload, ackOK bool) (*bytecodeUpload, *int) {
	flushes := 0
	u := &bytecodeUpload{
		store:  store,
		bucket: "assets-xyz",
		key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
		root:   ack.Dir,
		flush: func(context.Context) (compileCacheFlushedPayload, bool) {
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

		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: nodeDir, OK: true}, true)
		u.root = root

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
		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

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
		store := &fakeBytecodeStore{exists: true}
		u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

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
			ack   compileCacheFlushedPayload
			ackOK bool
		}{
			{name: "node answered not-ok", ack: compileCacheFlushedPayload{OK: false}, ackOK: true},
			{name: "node never answered", ackOK: false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := &fakeBytecodeStore{}
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
		store := &fakeBytecodeStore{}
		u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: t.TempDir(), OK: true}, true)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin/2))
		defer cancel()
		u.run(ctx)

		if *flushes != 0 {
			t.Errorf("flushes = %d, want the child left alone", *flushes)
		}
		if len(store.heads) != 0 || len(store.puts) != 0 {
			t.Errorf("touched S3 (heads=%v puts=%v), want nothing", store.heads, store.puts)
		}
	})

	t.Run("skips an empty cache directory", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: t.TempDir(), OK: true}, true)

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
		if err := f.Truncate(bytecodeCacheCeiling + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()

		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

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
			store *fakeBytecodeStore
		}{
			{name: "head fails", store: &fakeBytecodeStore{headErr: errors.New("access denied")}},
			{name: "put fails", store: &fakeBytecodeStore{putErr: errors.New("slow down")}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u, _ := uploadFixture(tc.store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
				u.run(context.Background())
			})
		}
	})

	t.Run("bounds both store calls by the budget", func(t *testing.T) {
		for _, blockOn := range []string{"head", "put"} {
			t.Run(blockOn, func(t *testing.T) {
				ctx, spendBudget := context.WithCancel(context.Background())
				defer spendBudget()

				store := &blockingBytecodeStore{blockOn: blockOn, spendBudget: spendBudget}
				u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)

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
		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

		u.flush = func(ctx context.Context) (compileCacheFlushedPayload, bool) {
			<-ctx.Done()
			return compileCacheFlushedPayload{Dir: dir, OK: true}, true
		}

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+100*time.Millisecond))
		defer cancel()

		start := time.Now()
		u.run(ctx)
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s, want the attempt abandoned at the budget", elapsed)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want none once the budget is spent", store.puts)
		}
	})

	t.Run("abandons an archive build in flight", func(t *testing.T) {
		dir, baseline := bigCacheDir(t)
		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+baseline/5))
		defer cancel()

		start := time.Now()
		u.run(ctx)
		elapsed := time.Since(start)

		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want nothing once the build was abandoned", store.puts)
		}
		if elapsed > baseline {
			t.Errorf("took %s against a %s full build, want the attempt abandoned partway", elapsed, baseline)
		}
	})

	t.Run("is rooted at the dir compile cache env declares", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		env := compileCacheEnv()
		if len(env) != 1 || !strings.HasPrefix(env[0], "NODE_COMPILE_CACHE=") {
			t.Fatalf("compileCacheEnv() = %v, want exactly one NODE_COMPILE_CACHE entry", env)
		}
		dir := strings.TrimPrefix(env[0], "NODE_COMPILE_CACHE=")

		r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
		u := r.upload(func(context.Context) (compileCacheFlushedPayload, bool) {
			return compileCacheFlushedPayload{}, false
		})
		if u.root != dir {
			t.Errorf("upload root = %q, want %q: the two legs disagree on where the cache lives", u.root, dir)
		}
	})
}

type blockingBytecodeStore struct {
	blockOn     string
	spendBudget context.CancelFunc
	reached     bool
}

func (b *blockingBytecodeStore) block(ctx context.Context) error {
	b.reached = true
	b.spendBudget()
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingBytecodeStore) objectExists(ctx context.Context, _, _ string) (bool, error) {
	if b.blockOn != "head" {
		return false, nil
	}
	return false, b.block(ctx)
}

func (b *blockingBytecodeStore) putObject(ctx context.Context, _, _ string, _ []byte) error {
	if b.blockOn != "put" {
		return nil
	}
	return b.block(ctx)
}

func (b *blockingBytecodeStore) getObject(context.Context, string, string) (io.ReadCloser, int64, error) {
	return nil, 0, errBytecodeCacheMiss
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
	if _, err := buildBytecodeArchive(context.Background(), dir); err != nil {
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

func TestUploadBytecodeCacheOnce(t *testing.T) {
	t.Run("runs at most once per instance", func(t *testing.T) {
		dir := cacheDirWith(t, "compiled bytes")
		store := &fakeBytecodeStore{}
		u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)
		m := &Membrane{bytecode: u}

		for range 3 {
			m.uploadBytecodeCacheOnce(context.Background())
		}

		if *flushes != 1 {
			t.Errorf("flushes = %d, want 1", *flushes)
		}
		if len(store.puts) != 1 {
			t.Errorf("puts = %d, want 1", len(store.puts))
		}
	})

	t.Run("does not retry after a failure", func(t *testing.T) {
		store := &fakeBytecodeStore{putErr: errors.New("access denied")}
		u, flushes := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
		m := &Membrane{bytecode: u}

		m.uploadBytecodeCacheOnce(context.Background())
		m.uploadBytecodeCacheOnce(context.Background())

		if *flushes != 1 {
			t.Errorf("flushes = %d, want 1", *flushes)
		}
	})

	t.Run("no ops for an unconfigured function", func(t *testing.T) {
		m := &Membrane{}
		m.uploadBytecodeCacheOnce(context.Background())
	})
}

func TestCompileCacheEnv(t *testing.T) {
	t.Run("gate closed", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "")
		if got := compileCacheEnv(); got != nil {
			t.Errorf("compileCacheEnv() = %v, want nil with no prefix configured", got)
		}
	})
	t.Run("gate open", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		want := []string{"NODE_COMPILE_CACHE=/tmp/.ocel/compile-cache"}
		got := compileCacheEnv()
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("compileCacheEnv() = %v, want %v", got, want)
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
				t.Setenv(bytecodePrefixEnvVar, tc.prefix)
				t.Setenv(bytecodeBucketEnvVar, tc.bucket)
				t.Setenv("AWS_LAMBDA_FUNCTION_NAME", tc.function)
				if got := resolveBytecodeResolution(context.Background(), nodeVersion); got != nil {
					t.Errorf("resolveBytecodeResolution() = %+v, want nil", got)
				}
			})
		}
	})

	t.Run("nil when the node version cannot be read", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		t.Setenv(bytecodeBucketEnvVar, "assets")
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
				if got := resolveBytecodeResolution(context.Background(), nodeVersion); got != nil {
					t.Errorf("resolveBytecodeResolution() = %+v, want nil", got)
				}
			})
		}
	})

	t.Run("carries the environment and version into the key", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "stg/proj/web/r1a2b3c4d/bytecode")
		t.Setenv(bytecodeBucketEnvVar, "assets-xyz")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolveBytecodeResolution(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolveBytecodeResolution() = nil, want a resolution for a fully configured function")
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
		t.Setenv(bytecodePrefixEnvVar, "ocel/stg")
		t.Setenv(bytecodeBucketEnvVar, "assets-xyz")
		t.Setenv("OCEL_ISR_BUCKET", "")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-express-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolveBytecodeResolution(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolveBytecodeResolution() = nil, want a resolution for a node framework function")
		}
		if r.bucket != "assets-xyz" {
			t.Errorf("bucket = %q, want %q", r.bucket, "assets-xyz")
		}
	})
}

func TestBytecodeResolution(t *testing.T) {
	t.Run("upload carries the same bucket and key", func(t *testing.T) {
		r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
		flushed := false
		flush := func(context.Context) (compileCacheFlushedPayload, bool) {
			flushed = true
			return compileCacheFlushedPayload{}, false
		}

		u := r.upload(flush)
		if u.store != r.store {
			t.Error("store is not the resolution's")
		}
		if u.bucket != r.bucket {
			t.Errorf("bucket = %q, want the resolution's %q", u.bucket, r.bucket)
		}
		if u.key != r.key {
			t.Errorf("key = %q, want the resolution's %q", u.key, r.key)
		}
		if u.flush == nil {
			t.Fatal("flush = nil, want the caller's")
		}
		u.flush(context.Background())
		if !flushed {
			t.Error("flush is not the one the caller passed")
		}
	})
}

func TestHandleInvocationBytecode(t *testing.T) {
	t.Run("uploads after completion and before the next next", func(t *testing.T) {
		node := okNode(t)
		rt, _ := fakeRuntime(t, []byte(getEvent))

		goSide, jsSide := net.Pipe()
		t.Cleanup(func() { goSide.Close(); jsSide.Close() })

		store := &fakeBytecodeStore{}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "compiled bytes"), OK: true}, true)
		m := &Membrane{
			nodePort:  portOf(t, node),
			client:    &http.Client{},
			control:   goSide,
			lifecycle: true,
			pending:   map[string]chan struct{}{},
			bytecode:  u,
		}
		go m.drainControl(bufio.NewReader(goSide))

		done := make(chan error, 1)
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		time.Sleep(75 * time.Millisecond)
		store.mu.Lock()
		early := len(store.puts)
		store.mu.Unlock()
		if early != 0 {
			t.Fatalf("puts before invocation-complete = %d, want the upload held until the invocation is done", early)
		}

		if _, err := jsSide.Write([]byte(`{"type":"invocation-complete","payload":{"requestId":"req-1"}}` + "\n")); err != nil {
			t.Fatalf("write control message: %v", err)
		}

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handleInvocation never returned")
		}

		if len(store.puts) != 1 {
			t.Errorf("puts = %d, want the cache published before the loop moved on", len(store.puts))
		}
	})
}

func controlConnPair(t *testing.T) (*Membrane, *bufio.Reader, net.Conn) {
	t.Helper()
	membraneSide, nodeSide := net.Pipe()
	t.Cleanup(func() { membraneSide.Close(); nodeSide.Close() })
	m := &Membrane{control: membraneSide, lifecycle: true, pending: map[string]chan struct{}{}}
	go m.drainControl(bufio.NewReader(membraneSide))
	return m, bufio.NewReader(nodeSide), nodeSide
}

type flushOutcome struct {
	ack compileCacheFlushedPayload
	ok  bool
}

func startFlush(m *Membrane, ctx context.Context) <-chan flushOutcome {
	done := make(chan flushOutcome, 1)
	go func() {
		ack, ok := m.flushCompileCache(ctx)
		done <- flushOutcome{ack: ack, ok: ok}
	}()
	return done
}

func TestFlushCompileCache(t *testing.T) {
	t.Run("delivers the ack to the waiter", func(t *testing.T) {
		m, nodeReader, nodeConn := controlConnPair(t)
		done := startFlush(m, context.Background())

		line, err := nodeReader.ReadString('\n')
		if err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		if line != flushCompileCacheLine {
			t.Errorf("node received %q, want %q", line, flushCompileCacheLine)
		}
		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/.ocel/compile-cache","ok":true}}`)

		got := <-done
		if !got.ok {
			t.Fatal("flushCompileCache() ok = false, want the ack delivered")
		}
		if got.ack.Dir != "/tmp/.ocel/compile-cache" || !got.ack.OK {
			t.Errorf("ack = %+v, want the dir and ok node reported", got.ack)
		}
	})

	t.Run("carries a null dir as not OK", func(t *testing.T) {
		m, nodeReader, nodeConn := controlConnPair(t)
		done := startFlush(m, context.Background())

		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":null,"ok":false}}`)

		got := <-done
		if !got.ok {
			t.Fatal("flushCompileCache() ok = false, want the ack delivered")
		}
		if got.ack.Dir != "" || got.ack.OK {
			t.Errorf("ack = %+v, want an empty dir and ok=false", got.ack)
		}
	})

	t.Run("gives up when the child never answers", func(t *testing.T) {
		m, nodeReader, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		done := startFlush(m, ctx)
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}

		if got := <-done; got.ok {
			t.Error("flushCompileCache() ok = true, want a give-up")
		}
		if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
			t.Errorf("took %s, want the context to end the wait early", elapsed)
		}
	})

	t.Run("gives up when the child never reads the request", func(t *testing.T) {
		m, _, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		if _, ok := m.flushCompileCache(ctx); ok {
			t.Error("flushCompileCache() ok = true, want a give-up")
		}
		if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
			t.Errorf("took %s, want the write deadline to end the attempt", elapsed)
		}
	})

	t.Run("clears the write deadline afterwards", func(t *testing.T) {
		m, nodeReader, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		done := startFlush(m, ctx)
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		<-done

		time.Sleep(100 * time.Millisecond)
		written := make(chan error, 1)
		go func() {
			_, err := m.control.Write([]byte("{\"type\":\"liveValues\"}\n"))
			written <- err
		}()
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("a later push never arrived: %v", err)
		}
		if err := <-written; err != nil {
			t.Errorf("a later push failed on the flush's stale deadline: %v", err)
		}
	})

	t.Run("no ops without a control connection", func(t *testing.T) {
		m := &Membrane{}
		if _, ok := m.flushCompileCache(context.Background()); ok {
			t.Error("flushCompileCache() ok = true, want false with no child attached")
		}
	})
}

func TestDrainControl(t *testing.T) {
	t.Run("drops an unawaited flush ack", func(t *testing.T) {
		m, _, nodeConn := controlConnPair(t)
		waiter := m.registerWaiter("req-1")

		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/x","ok":true}}`)
		fmt.Fprintln(nodeConn, `{"type":"invocation-complete","payload":{"requestId":"req-1"}}`)

		select {
		case <-waiter:
		case <-time.After(2 * time.Second):
			t.Fatal("drain loop stalled on an ack with no waiter")
		}
	})
}

func TestBytecodeBudget(t *testing.T) {
	t.Run("no deadline yields the cap", func(t *testing.T) {
		if got := bytecodeBudget(context.Background()); got != bytecodeUploadBudget {
			t.Errorf("bytecodeBudget() = %s, want %s", got, bytecodeUploadBudget)
		}
	})
	t.Run("a distant deadline is capped", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()
		if got := bytecodeBudget(ctx); got != bytecodeUploadBudget {
			t.Errorf("bytecodeBudget() = %s, want %s", got, bytecodeUploadBudget)
		}
	})
	t.Run("a near deadline leaves the completion margin", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin+time.Second))
		defer cancel()
		got := bytecodeBudget(ctx)
		if got <= 0 || got > time.Second {
			t.Errorf("bytecodeBudget() = %s, want a positive budget under 1s", got)
		}
	})
	t.Run("a deadline inside the margin yields nothing", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(completionMargin/2))
		defer cancel()
		if got := bytecodeBudget(ctx); got > 0 {
			t.Errorf("bytecodeBudget() = %s, want a non-positive budget", got)
		}
	})
}

func TestNodeChildEnv(t *testing.T) {
	t.Run("carries the compile cache only when gated", func(t *testing.T) {
		const want = "NODE_COMPILE_CACHE=/tmp/.ocel/compile-cache"

		t.Run("gate open", func(t *testing.T) {
			t.Setenv(bytecodePrefixEnvVar, "ocel")
			env := nodeChildEnv("/tmp/ocel-control.sock", []string{"OCEL_BAKED_X=1"})
			if !slices.Contains(env, want) {
				t.Errorf("child env has no %q: %v", want, env)
			}
			if !slices.Contains(env, "OCEL_CONTROL_SOCKET=/tmp/ocel-control.sock") {
				t.Error("child env lost the control socket")
			}
			if !slices.Contains(env, "OCEL_BAKED_X=1") {
				t.Error("child env lost the caller's extra entries")
			}
		})

		t.Run("gate closed", func(t *testing.T) {
			t.Setenv(bytecodePrefixEnvVar, "")
			for _, e := range nodeChildEnv("/tmp/ocel-control.sock", nil) {
				if strings.HasPrefix(e, "NODE_COMPILE_CACHE=") {
					t.Errorf("child env has %q with the gate closed", e)
				}
			}
		})
	})
}

func s3Store(t *testing.T, handler http.HandlerFunc) (bytecodeStore, *httptest.Server) {
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
	return s3BytecodeStore{client: client}, srv
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
				store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
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
		store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
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
		store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

		if err := store.putObject(context.Background(), "assets-xyz", "some/key", []byte("x")); err == nil {
			t.Error("putObject() error = nil, want the failure reported")
		}
	})

	t.Run("get object", func(t *testing.T) {
		t.Run("200 returns the body and content length", func(t *testing.T) {
			store, _ := s3Store(t, func(w http.ResponseWriter, r *http.Request) {
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
			store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "missing/key")
			if !errors.Is(err, errBytecodeCacheMiss) {
				t.Errorf("getObject() error = %v, want errBytecodeCacheMiss", err)
			}
		})

		t.Run("403 is a real error, not an absence", func(t *testing.T) {
			store, _ := s3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "some/key")
			if err == nil {
				t.Fatal("getObject() error = nil, want the failure reported")
			}
			if errors.Is(err, errBytecodeCacheMiss) {
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

func buildArchive(t *testing.T, entries []tarEntry) []byte {
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
					archive = buildArchive(t, []tarEntry{{name: name, typeflag: tar.TypeReg, content: []byte("abc")}})
				}

				dest := filepath.Join(t.TempDir(), "cache")
				if _, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, bytecodeCacheCeiling); err == nil {
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

		archive, err := buildBytecodeArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
		}

		dest := t.TempDir()
		n, err := untarGzipInto(context.Background(), bytes.NewReader(archive), dest, bytecodeCacheCeiling)
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
		if _, err := untarGzipInto(context.Background(), bytes.NewReader(buf.Bytes()), dest, bytecodeCacheCeiling); err != nil {
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
		archive := buildArchive(t, []tarEntry{
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
		archive := buildArchive(t, entries)

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

func rehydrateFixture(archive []byte) *fakeBytecodeStore {
	return &fakeBytecodeStore{getBody: io.NopCloser(bytes.NewReader(archive)), getSize: int64(len(archive))}
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
		archive, err := buildBytecodeArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
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
		store := &fakeBytecodeStore{getErr: errBytecodeCacheMiss}
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
			store := &fakeBytecodeStore{getErr: errBytecodeCacheMiss}
			rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		})
		if !strings.Contains(missOut, "nothing to rehydrate") {
			t.Errorf("miss log = %q, want it to name a miss", missOut)
		}
		if strings.Contains(missOut, "could not") {
			t.Errorf("miss log = %q, want no failure wording for the expected first-cold-start case", missOut)
		}

		failOut := captureStderr(t, func() {
			store := &fakeBytecodeStore{getErr: errors.New("access denied")}
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
		store := &fakeBytecodeStore{getBody: io.NopCloser(bytes.NewReader(body)), getSize: int64(len(body))}
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
		archive := buildArchive(t, []tarEntry{{name: "../escape", typeflag: tar.TypeReg, content: []byte("hax")}})
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
		archive := buildArchive(t, []tarEntry{{name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("hax")}})
		store := rehydrateFixture(archive)

		n, ok := rehydrateCompileCache(context.Background(), store, "bucket", "key", dest)
		if ok || n != 0 {
			t.Errorf("rehydrateCompileCache() = (%d, %v), want (0, false) for an absolute path", n, ok)
		}
		assertNoCacheDir(t, dest)
	})

	t.Run("rejects symlink", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "cache")
		archive := buildArchive(t, []tarEntry{{name: "link.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}})
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
		store := &fakeBytecodeStore{
			getBody: io.NopCloser(poisonReader{t}),
			getSize: bytecodeCacheCeiling + 1,
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
		size := int64(bytecodeCacheCeiling) + 1<<20
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
		if store.getSize >= bytecodeCacheCeiling {
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

		archive := buildArchive(t, []tarEntry{{name: "fresh.bin", typeflag: tar.TypeReg, content: []byte("new")}})
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

		store := &fakeBytecodeStore{getBody: pr, getSize: 1 << 20}
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
		archive, err := buildBytecodeArchive(context.Background(), cacheDirWith(t, "compiled bytes"))
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
		}
		r := &bytecodeResolution{
			store:  rehydrateFixture(archive),
			bucket: "assets-xyz",
			key:    "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz",
		}
		dest := filepath.Join(t.TempDir(), "cache")

		var hit bool
		out := captureStderr(t, func() {
			hit = rehydrateBytecodeCache(context.Background(), r, dest)
		})
		if !hit {
			t.Fatal("rehydrateBytecodeCache() = false, want a hit")
		}
		if !strings.Contains(out, "ocel: rehydrated compile cache from ocel/bytecode/my-app/node24.3.1-arm64.tar.gz:") {
			t.Errorf("log = %q, want it to name the key", out)
		}
		if !strings.Contains(out, "bytes in") || !strings.Contains(out, "ms") {
			t.Errorf("log = %q, want bytes and elapsed ms", out)
		}
	})

	t.Run("miss logs nothing of its own", func(t *testing.T) {
		r := &bytecodeResolution{store: &fakeBytecodeStore{getErr: errBytecodeCacheMiss}, bucket: "b", key: "k"}
		dest := filepath.Join(t.TempDir(), "cache")

		var hit bool
		out := captureStderr(t, func() {
			hit = rehydrateBytecodeCache(context.Background(), r, dest)
		})
		if hit {
			t.Fatal("rehydrateBytecodeCache() = true, want a miss")
		}
		if strings.Count(out, "\n") != 1 {
			t.Errorf("log = %q, want exactly the one line rehydrateCompileCache already wrote", out)
		}
	})

	t.Run("applies its own budget", func(t *testing.T) {
		r := &bytecodeResolution{store: blockingGetStore{}, bucket: "b", key: "k"}
		dest := filepath.Join(t.TempDir(), "cache")

		done := make(chan bool, 1)
		go func() { done <- rehydrateBytecodeCache(context.Background(), r, dest) }()

		select {
		case hit := <-done:
			if hit {
				t.Error("rehydrateBytecodeCache() = true, want false once the store hangs past its budget")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("rehydrateBytecodeCache did not return within 3s, want bytecodeRehydrateBudget enforced")
		}
	})
}

type blockingGetStore struct{}

func (blockingGetStore) objectExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (blockingGetStore) putObject(context.Context, string, string, []byte) error { return nil }
func (blockingGetStore) getObject(ctx context.Context, _, _ string) (io.ReadCloser, int64, error) {
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func TestBytecodeRehydrate(t *testing.T) {
	t.Run("targets the dir compile cache env declares", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		env := compileCacheEnv()
		if len(env) != 1 || env[0] != "NODE_COMPILE_CACHE="+compileCacheDir {
			t.Fatalf("compileCacheEnv() = %v, want exactly [NODE_COMPILE_CACHE=%s]", env, compileCacheDir)
		}

		dir := filepath.Join(t.TempDir(), "compile-cache")
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "cached.blob"), []byte("compiled"), 0o644); err != nil {
			t.Fatal(err)
		}
		archive, err := buildBytecodeArchive(context.Background(), src)
		if err != nil {
			t.Fatalf("buildBytecodeArchive: %v", err)
		}
		r := &bytecodeResolution{store: rehydrateFixture(archive), bucket: "b", key: "k"}

		if !rehydrateBytecodeCache(context.Background(), r, dir) {
			t.Fatal("rehydrateBytecodeCache() = false, want a hit")
		}
		got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
		if err != nil || string(got) != "compiled" {
			t.Errorf("file at compileCacheEnv's dir = %q, %v, want %q, nil: rehydrateBytecodeCache wrote somewhere else", got, err, "compiled")
		}
	})
}

func fakeSpawn(gotBudget *time.Duration) spawner {
	return func(_ []string, budget time.Duration, onControl func(io.Writer), _ <-chan struct{}) (*Membrane, error) {
		*gotBudget = budget
		if onControl != nil {
			onControl(&sink{})
		}
		return &Membrane{}, nil
	}
}

func TestBringUpWithBytecode(t *testing.T) {
	t.Run("rehydrations cost is carved out of startup budget", func(t *testing.T) {
		const rehydrateCost = 200 * time.Millisecond

		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

		rehydrate := func(context.Context, *bytecodeResolution) bool {
			time.Sleep(rehydrateCost)
			return false
		}

		var gotBudget time.Duration
		start := time.Now()
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if membrane.bytecode == nil {
			t.Fatal("bytecode = nil, want the upload leg attached on a miss")
		}

		if gotBudget > startupBudget-rehydrateCost/2 {
			t.Errorf("budget handed to bringUp = %s, want it reduced by rehydration's %s cost", gotBudget, rehydrateCost)
		}
	})

	t.Run("floors the spawn budget", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
		rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

		start := time.Now().Add(-2 * startupBudget)

		var gotBudget time.Duration
		if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate); err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if gotBudget != minSpawnBudget {
			t.Errorf("budget handed to bringUp = %s, want the floor of %s", gotBudget, minSpawnBudget)
		}
	})

	t.Run("normal carve out is unaffected by the floor", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}
		rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

		start := time.Now()
		var gotBudget time.Duration
		if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate); err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if gotBudget <= minSpawnBudget {
			t.Errorf("budget handed to bringUp = %s, want it well above the floor for a start this recent", gotBudget)
		}
		if gotBudget > startupBudget {
			t.Errorf("budget handed to bringUp = %s, want it at most startupBudget", gotBudget)
		}
	})

	t.Run("a hit disables the upload leg", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

		rehydrateCalls := 0
		rehydrate := func(context.Context, *bytecodeResolution) bool {
			rehydrateCalls++
			return true
		}

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if rehydrateCalls != 1 {
			t.Errorf("rehydrate calls = %d, want 1", rehydrateCalls)
		}
		if membrane.bytecode != nil {
			t.Error("bytecode != nil, want the upload leg disabled after a rehydrate hit")
		}
	})

	t.Run("a miss attaches the upload leg with the same key", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
		bytecodeReady <- r

		rehydrate := func(context.Context, *bytecodeResolution) bool { return false }

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if membrane.bytecode == nil {
			t.Fatal("bytecode = nil, want the upload leg attached on a miss")
		}
		if membrane.bytecode.bucket != r.bucket || membrane.bytecode.key != r.key {
			t.Errorf("upload bucket/key = %s/%s, want the resolution's %s/%s",
				membrane.bytecode.bucket, membrane.bytecode.key, r.bucket, r.key)
		}
	})

	t.Run("nil resolution skips both legs", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- nil

		rehydrateCalls := 0
		rehydrate := func(context.Context, *bytecodeResolution) bool {
			rehydrateCalls++
			return false
		}

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if rehydrateCalls != 0 {
			t.Errorf("rehydrate calls = %d, want 0 for an unconfigured deployment", rehydrateCalls)
		}
		if membrane.bytecode != nil {
			t.Error("bytecode != nil, want no upload leg for an unconfigured deployment")
		}
	})

	t.Run("a resolution that never arrives does not block the spawn", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution)

		rehydrateCalls := 0
		rehydrate := func(context.Context, *bytecodeResolution) bool {
			rehydrateCalls++
			return false
		}

		var gotBudget time.Duration
		start := time.Now()
		done := make(chan struct{})
		var membrane *Membrane
		var err error
		go func() {
			membrane, err = bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, start, bytecodeReady, neverEmbedded, rehydrate)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(startupBudget):
			t.Fatal("bringUpWithBytecode never returned; a resolution that never arrived blocked the spawn")
		}
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if rehydrateCalls != 0 {
			t.Errorf("rehydrate calls = %d, want 0 when the resolution never arrived", rehydrateCalls)
		}
		if membrane.bytecode != nil {
			t.Error("bytecode != nil, want no upload leg when the resolution never arrived")
		}
		if gotBudget <= 0 {
			t.Errorf("budget handed to bringUp = %s, want a positive budget rather than one driven negative", gotBudget)
		}
		if gotBudget >= startupBudget {
			t.Errorf("budget handed to bringUp = %s, want it reduced by the time spent waiting on the resolution", gotBudget)
		}
	})

	t.Run("an embedded hit skips the S3 leg", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		r := &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}
		bytecodeReady <- r

		embedded := func(context.Context, *bytecodeResolution) bool { return true }
		rehydrateCalls := 0
		rehydrate := func(context.Context, *bytecodeResolution) bool {
			rehydrateCalls++
			return true
		}

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if rehydrateCalls != 0 {
			t.Errorf("rehydrate calls = %d, want the S3 leg skipped entirely", rehydrateCalls)
		}
		if membrane.bytecode != nil {
			t.Error("bytecode != nil, want the upload leg disabled after an embedded hit")
		}
		if !membrane.bytecodeCached() {
			t.Error("bytecodeCached() = false, want the hit recorded")
		}
		if membrane.bytecodeSource != bytecodeSourceEmbedded {
			t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceEmbedded)
		}
		if membrane.bytecodeKey != r.key {
			t.Errorf("bytecodeKey = %q, want the resolution's %q", membrane.bytecodeKey, r.key)
		}
	})

	t.Run("no embedded copy falls back to S3", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"}

		rehydrate := func(context.Context, *bytecodeResolution) bool { return true }

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded, rehydrate)
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if membrane.bytecodeSource != bytecodeSourceS3 {
			t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceS3)
		}
		if membrane.bytecode != nil {
			t.Error("bytecode != nil, want the upload leg disabled after an S3 hit")
		}
	})

	t.Run("miss on both legs reports no source", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, neverEmbedded,
			func(context.Context, *bytecodeResolution) bool { return false })
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if membrane.bytecodeSource != bytecodeSourceNone {
			t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceNone)
		}
		if membrane.bytecodeCached() {
			t.Error("bytecodeCached() = true, want a miss on both legs")
		}
		if membrane.bytecode == nil {
			t.Fatal("bytecode = nil, want the upload leg armed on a miss")
		}
	})

	t.Run("a failed embedded attempt leaves the S3 leg the remaining budget", func(t *testing.T) {
		const embeddedCost = 300 * time.Millisecond

		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- &bytecodeResolution{store: &fakeBytecodeStore{}, bucket: "b", key: "k"}

		embedded := func(ctx context.Context, _ *bytecodeResolution) bool {
			time.Sleep(embeddedCost)
			return false
		}
		var remaining time.Duration
		rehydrate := func(ctx context.Context, _ *bytecodeResolution) bool {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("the S3 leg was handed a context with no deadline, want the shared rehydrate budget")
				return false
			}
			remaining = time.Until(deadline)
			return true
		}

		var gotBudget time.Duration
		if _, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded, rehydrate); err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if remaining > bytecodeRehydrateBudget-embeddedCost/2 {
			t.Errorf("S3 leg budget = %s, want it short by the %s the embedded attempt spent", remaining, embeddedCost)
		}
		if remaining <= 0 {
			t.Errorf("S3 leg budget = %s, want what remains of the shared budget", remaining)
		}
	})

	t.Run("nil resolution skips the embedded leg too", func(t *testing.T) {
		l := newLiveValues(resolves(nil), nil, nil, nil)
		bytecodeReady := make(chan *bytecodeResolution, 1)
		bytecodeReady <- nil

		embeddedCalls := 0
		embedded := func(context.Context, *bytecodeResolution) bool {
			embeddedCalls++
			return true
		}

		var gotBudget time.Duration
		membrane, err := bringUpWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, l.start(context.Background()), nil, time.Now(), bytecodeReady, embedded,
			func(context.Context, *bytecodeResolution) bool { return false })
		if err != nil {
			t.Fatalf("bringUpWithBytecode: %v", err)
		}
		if embeddedCalls != 0 {
			t.Errorf("embedded calls = %d, want 0 for an unconfigured deployment", embeddedCalls)
		}
		if membrane.bytecodeSource != bytecodeSourceNone {
			t.Errorf("bytecodeSource = %q, want %q", membrane.bytecodeSource, bytecodeSourceNone)
		}
		if membrane.bytecodeKey != "" {
			t.Errorf("bytecodeKey = %q, want none for an unconfigured deployment", membrane.bytecodeKey)
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
			{bytecodeCacheKey("ocel", "my-app", "24.3.1", "arm64"), "/var/task/.ocel/bytecode/node24.3.1-arm64.tar"},
			{bytecodeCacheKey("stg/deploy", "other-fn", "20.11.0", "amd64"), "/var/task/.ocel/bytecode/node20.11.0-x86_64.tar"},
		}
		for _, c := range cases {
			if got := embeddedBytecodePath(c.key); got != c.want {
				t.Errorf("embeddedBytecodePath(%q) = %q, want %q", c.key, got, c.want)
			}
		}
	})

	t.Run("empty for a key that is not a cache tarball", func(t *testing.T) {
		for _, key := range []string{"", "ocel/bytecode/my-app", "ocel/bytecode/my-app/node24.3.1-arm64.tar", "some/other/object.zip"} {
			if got := embeddedBytecodePath(key); got != "" {
				t.Errorf("embeddedBytecodePath(%q) = %q, want no path at all", key, got)
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
		n, hit := loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
		if !hit {
			t.Fatal("loadEmbeddedBytecodeCache() = false, want a hit")
		}
		var want int64
		for _, contents := range files {
			want += int64(len(contents)) + tarEntryOverhead
		}
		if n != want {
			t.Errorf("loadEmbeddedBytecodeCache() = %d bytes, want %d", n, want)
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
			_, hit = loadEmbeddedBytecodeCache(context.Background(), filepath.Join(t.TempDir(), "nothing-here.tar"), dest)
		})
		if hit {
			t.Fatal("loadEmbeddedBytecodeCache() = true, want a miss")
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
			_, hit = loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbeddedBytecodeCache() = true, want a corrupt tar reported as a miss")
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
					_, hit = loadEmbeddedBytecodeCache(context.Background(), tarPath, dest)
				})
				if hit {
					t.Fatalf("loadEmbeddedBytecodeCache() = true for a %s entry, want it rejected", name)
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
			_, hit = loadEmbeddedBytecodeCache(ctx, tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbeddedBytecodeCache() = true, want a spent budget to skip the attempt")
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
			_, hit = loadEmbeddedBytecodeCache(ctx, tarPath, dest)
		})
		if hit {
			t.Fatal("loadEmbeddedBytecodeCache() = true, want the extraction cut off by the shared budget")
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
		dir := filepath.Join(root, ".ocel", "bytecode")
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
			hit = embeddedBytecodeCache(context.Background(), tarPath, dest)
		})
		if !hit {
			t.Fatal("embeddedBytecodeCache() = false, want a hit")
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

func neverEmbedded(context.Context, *bytecodeResolution) bool { return false }

func TestBytecodeEmbedded(t *testing.T) {
	t.Run("targets the dir compile cache env declares", func(t *testing.T) {
		t.Setenv(bytecodePrefixEnvVar, "ocel")
		env := compileCacheEnv()
		if len(env) != 1 || env[0] != "NODE_COMPILE_CACHE="+compileCacheDir {
			t.Fatalf("compileCacheEnv() = %v, want exactly [NODE_COMPILE_CACHE=%s]", env, compileCacheDir)
		}

		dir := filepath.Join(t.TempDir(), "compile-cache")
		tarPath := filepath.Join(t.TempDir(), "node24.3.1-arm64.tar")
		if err := os.WriteFile(tarPath, buildTar(t, []tarEntry{{name: "cached.blob", typeflag: tar.TypeReg, content: []byte("compiled")}}), 0o644); err != nil {
			t.Fatal(err)
		}
		captureStderr(t, func() {
			if !embeddedBytecodeCache(context.Background(), tarPath, dir) {
				t.Error("embeddedBytecodeCache() = false, want a hit")
			}
		})
		got, err := os.ReadFile(filepath.Join(dir, "cached.blob"))
		if err != nil || string(got) != "compiled" {
			t.Errorf("file at compileCacheEnv's dir = %q, %v, want %q, nil", got, err, "compiled")
		}
	})
}
