package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBytecodeCacheKey(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		functionName string
		nodeMajor    int
		goArch       string
		want         string
	}{
		{
			name:         "amd64 maps to the AWS x86_64 spelling",
			prefix:       "ocel",
			functionName: "my-app",
			nodeMajor:    24,
			goArch:       "amd64",
			want:         "ocel/bytecode/my-app/node24-x86_64.tar.gz",
		},
		{
			name:         "arm64 passes through unchanged",
			prefix:       "ocel",
			functionName: "my-app",
			nodeMajor:    24,
			goArch:       "arm64",
			want:         "ocel/bytecode/my-app/node24-arm64.tar.gz",
		},
		{
			name:         "an unrecognized arch still passes through",
			prefix:       "stg/deploy",
			functionName: "other-fn",
			nodeMajor:    20,
			goArch:       "riscv64",
			want:         "stg/deploy/bytecode/other-fn/node20-riscv64.tar.gz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bytecodeCacheKey(tc.prefix, tc.functionName, tc.nodeMajor, tc.goArch)
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

func TestNodeMajor(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    int
		wantErr bool
	}{
		{name: "v-prefixed semver", version: "v24.3.1", want: 24},
		{name: "no v prefix", version: "20.11.0", want: 20},
		{name: "double digit major", version: "v18.19.1", want: 18},
		{name: "empty string", version: "", wantErr: true},
		{name: "not a version at all", version: "latest", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nodeMajor(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("nodeMajor(%q) = %d, nil, want an error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("nodeMajor(%q) unexpected error: %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("nodeMajor(%q) = %d, want %d", tc.version, got, tc.want)
			}
		})
	}
}

// readArchive extracts an archive back into a name -> contents map, for tests
// that need to assert on what buildBytecodeArchive actually wrote.
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

// TestBuildBytecodeArchive_RoundTrip proves the archive built from a compile
// cache directory reads back with the standard library's own tar+gzip readers
// and preserves the version-hash subdirectory Node nests its output under,
// using relative paths only.
func TestBuildBytecodeArchive_RoundTrip(t *testing.T) {
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

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}

	var wantSize int64
	for _, contents := range files {
		wantSize += int64(len(contents))
	}
	if archive.UncompressedSize != wantSize {
		t.Errorf("UncompressedSize = %d, want %d", archive.UncompressedSize, wantSize)
	}

	got := readArchive(t, archive.Data)
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
}

// TestBuildBytecodeArchive_SkipsNonRegularFiles proves a symlink planted in
// the compile cache directory is left out of the archive rather than
// followed or stored as a link, since only regular files are meant to travel.
func TestBuildBytecodeArchive_SkipsNonRegularFiles(t *testing.T) {
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

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	got := readArchive(t, archive.Data)
	if _, ok := got["link.bin"]; ok {
		t.Errorf("archive contains symlink link.bin, want it skipped")
	}
	if _, ok := got["subdir"]; ok {
		t.Errorf("archive contains directory entry subdir, want it skipped")
	}
	if got["real.bin"] != "real contents" {
		t.Errorf("archive[real.bin] = %q, want %q", got["real.bin"], "real contents")
	}
}

// TestBuildBytecodeArchive_MissingDirectoryIsEmptyNotAnError proves a
// function that has not written a compile cache yet (or whose /tmp was wiped)
// gets an empty archive back, not a failure the caller has to distinguish
// from a real error before deciding whether to skip the upload.
func TestBuildBytecodeArchive_MissingDirectoryIsEmptyNotAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	archive, err := buildBytecodeArchive(dir)
	if err != nil {
		t.Fatalf("buildBytecodeArchive: %v", err)
	}
	if archive.UncompressedSize != 0 {
		t.Errorf("UncompressedSize = %d, want 0", archive.UncompressedSize)
	}
	got := readArchive(t, archive.Data)
	if len(got) != 0 {
		t.Errorf("archive has %d entries, want 0: %v", len(got), got)
	}
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
