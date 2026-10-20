package bytecode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
		if err := os.Symlink(target, filepath.Join(dir, "binding.bin")); err != nil {
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
		if _, ok := got["binding.bin"]; ok {
			t.Errorf("archive contains symlink binding.bin, want it skipped")
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

func cacheDirWith(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cached.blob"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
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

	t.Run("includes the environment and version in the key", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "stg/proj/web/r1a2b3c4d/bytecode")
		t.Setenv(bucketEnvVar, "artifacts-xyz")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolve(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolve() = nil, want a resolution for a fully configured function")
		}
		if r.bucket != "artifacts-xyz" {
			t.Errorf("bucket = %q, want %q", r.bucket, "artifacts-xyz")
		}
		if r.store == nil {
			t.Error("store = nil, want an S3-backed store")
		}
		want := "stg/proj/web/r1a2b3c4d/bytecode/my-app/node24.3.1-" + s3Arch(runtime.GOARCH) + ".tar.gz"
		if r.key != want {
			t.Errorf("key = %q, want %q", r.key, want)
		}
	})

	t.Run("resolves for a function with no ISR cache", func(t *testing.T) {
		t.Setenv(prefixEnvVar, "ocel/stg")
		t.Setenv(bucketEnvVar, "artifacts-xyz")
		t.Setenv("OCEL_EDGE_BUCKET", "")
		t.Setenv("OCEL_CACHE_BUCKET", "")
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-express-app")
		t.Setenv("AWS_REGION", "us-east-1")

		nodeVersion := func(context.Context) (string, error) { return "v24.3.1", nil }

		r := resolve(context.Background(), nodeVersion)
		if r == nil {
			t.Fatal("resolve() = nil, want a resolution for a node runtime function")
		}
		if r.bucket != "artifacts-xyz" {
			t.Errorf("bucket = %q, want %q", r.bucket, "artifacts-xyz")
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
