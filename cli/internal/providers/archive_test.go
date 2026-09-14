package providers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type entry struct {
	name string
	mode os.FileMode
	kind byte
	body string
}

func regular(name, body string) entry {
	return entry{name: name, mode: 0o755, kind: tar.TypeReg, body: body}
}

func tarball(t *testing.T, members ...entry) string {
	t.Helper()

	var raw bytes.Buffer
	compressed := gzip.NewWriter(&raw)
	writer := tar.NewWriter(compressed)
	for _, held := range members {
		header := &tar.Header{Name: held.name, Typeflag: held.kind, Mode: int64(held.mode)}
		if held.kind == tar.TypeSymlink {
			header.Linkname = held.body
		} else {
			header.Size = int64(len(held.body))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write the header for %q: %v", held.name, err)
		}
		if held.kind == tar.TypeReg {
			if _, err := writer.Write([]byte(held.body)); err != nil {
				t.Fatalf("write the body of %q: %v", held.name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the tar: %v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("close the gzip: %v", err)
	}

	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatalf("write the archive: %v", err)
	}
	return path
}

func zipped(t *testing.T, members ...entry) string {
	t.Helper()

	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for _, held := range members {
		header := &zip.FileHeader{Name: held.name, Method: zip.Deflate}
		header.SetMode(held.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("write the header for %q: %v", held.name, err)
		}
		if _, err := entry.Write([]byte(held.body)); err != nil {
			t.Fatalf("write the body of %q: %v", held.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the zip: %v", err)
	}

	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatalf("write the archive: %v", err)
	}
	return path
}

func TestAMemberThatLandsOutsideTheDirectoryIsRefused(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	for _, name := range []string{
		"../escaped",
		"..",
		"provider/../../escaped",
		"a/b/../../../escaped",
		"",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path, err := member(into, name)
			if err == nil {
				t.Fatalf("member(%q) = %q, want it refused for landing outside %s", name, path, into)
			}
			if !strings.Contains(err.Error(), "outside the directory") {
				t.Errorf("member(%q) error = %q, want it to say where the member would land", name, err)
			}
		})
	}
}

func TestAnAbsoluteMemberIsPinnedUnderTheDirectoryRatherThanRefused(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	got, err := member(into, "/etc/passwd")
	if err != nil {
		t.Fatalf("member() error = %v", err)
	}
	if want := filepath.Join(into, "etc", "passwd"); got != want {
		t.Errorf("member() = %q, want %q: an absolute name is read as relative to the unpack directory", got, want)
	}
}

func TestATarThatClimbsOutOfTheDirectoryUnpacksNothing(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	archive := tarball(t, regular("../escaped", "owned"))

	if err := untar(archive, into); err == nil {
		t.Fatal("untar() error = nil, want the escaping member refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(into), "escaped")); !os.IsNotExist(err) {
		t.Errorf("a file was written beside %s (stat err = %v), want nothing outside it", into, err)
	}
}

func TestAZipThatClimbsOutOfTheDirectoryUnpacksNothing(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	archive := zipped(t, entry{name: "../escaped", mode: 0o644, body: "owned"})

	if err := unzip(archive, into); err == nil {
		t.Fatal("unzip() error = nil, want the escaping member refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(into), "escaped")); !os.IsNotExist(err) {
		t.Errorf("a file was written beside %s (stat err = %v), want nothing outside it", into, err)
	}
}

func TestASymlinkMemberOfATarIsSkipped(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	archive := tarball(t,
		entry{name: "ocel-provider-vps", mode: 0o777, kind: tar.TypeSymlink, body: "/etc/passwd"},
		regular("README", "hello"),
	)

	if err := untar(archive, into); err != nil {
		t.Fatalf("untar() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(into, "ocel-provider-vps")); !os.IsNotExist(err) {
		t.Errorf("the symlink member was unpacked (lstat err = %v), want only regular files taken", err)
	}
	if got := readUnpacked(t, into, "README"); got != "hello" {
		t.Errorf("README = %q, want the regular members either side of it still unpacked", got)
	}
}

func TestASymlinkMemberOfAZipBecomesARegularFileNeverALink(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	archive := zipped(t, entry{name: "ocel-provider-vps.exe", mode: os.ModeSymlink | 0o777, body: "/etc/passwd"})

	if err := unzip(archive, into); err != nil {
		t.Fatalf("unzip() error = %v", err)
	}

	info, err := os.Lstat(filepath.Join(into, "ocel-provider-vps.exe"))
	if err != nil {
		t.Fatalf("lstat the member: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the member is a symlink to %q, want a zip never able to plant a link", "/etc/passwd")
	}
	if got := readUnpacked(t, into, "ocel-provider-vps.exe"); got != "/etc/passwd" {
		t.Errorf("the member holds %q, want the link target kept as the file's own content", got)
	}
}

func TestARegularMemberIsUnpackedWithItsMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		archive func(*testing.T, ...entry) string
		unpack  func(string, string) error
	}{
		{"tar.gz", tarball, untar},
		{"zip", zipped, unzip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			into := filepath.Join(t.TempDir(), "unpacked")
			archive := tc.archive(t, regular("bin/ocel-provider-vps", "binary"))

			if err := tc.unpack(archive, into); err != nil {
				t.Fatalf("unpack error = %v", err)
			}
			if got := readUnpacked(t, into, "bin/ocel-provider-vps"); got != "binary" {
				t.Errorf("member = %q, want %q", got, "binary")
			}
			if runtime.GOOS == "windows" {
				return
			}
			info, err := os.Stat(filepath.Join(into, "bin", "ocel-provider-vps"))
			if err != nil {
				t.Fatalf("stat the member: %v", err)
			}
			if info.Mode().Perm() != 0o755 {
				t.Errorf("member mode = %v, want the executable bit the archive named kept", info.Mode().Perm())
			}
		})
	}
}

func TestAMemberNamedTwiceNeverOverwritesTheFirst(t *testing.T) {
	t.Parallel()

	into := filepath.Join(t.TempDir(), "unpacked")
	archive := tarball(t, regular("ocel-provider-vps", "first"), regular("ocel-provider-vps", "second"))

	if err := untar(archive, into); err == nil {
		t.Fatal("untar() error = nil, want a second member claiming a name already taken refused")
	}
	if got := readUnpacked(t, into, "ocel-provider-vps"); got != "first" {
		t.Errorf("member = %q, want the first member left as it was written", got)
	}
}

func readUnpacked(t *testing.T, into, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(into, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read the unpacked %s: %v", name, err)
	}
	return string(content)
}
