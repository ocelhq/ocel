package bytecode

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

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
			"symlink":       {name: "binding.bin", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
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
		dir := filepath.Join(root, buildoutput.StateDir, "bytecode")
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
