package bytecode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
