package bytecode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		archive := tarBytes(t, []tarEntry{{name: "binding.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}})
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
		compileCacheOn(t)
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
