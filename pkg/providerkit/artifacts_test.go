package providerkit

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func builtTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func walked(t *testing.T, dir string) []string {
	t.Helper()
	rels, err := artifactFiles(dir)
	if err != nil {
		t.Fatalf("artifactFiles: %v", err)
	}
	return rels
}

func digested(t *testing.T, dir string, overlay map[string][]byte) string {
	t.Helper()
	sum, err := digestArtifact(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("digestArtifact: %v", err)
	}
	return sum
}

func TestAnArtifactDigestNamesOneTreeAndOneOverlay(t *testing.T) {
	files := map[string]string{"src/server.js": "handler", "package.json": `{"name":"app"}`}
	sealed := map[string][]byte{".ocel/vars.sealed": []byte("one")}

	t.Run("one tree built twice digests the same", func(t *testing.T) {
		if a, b := digested(t, builtTree(t, files), nil), digested(t, builtTree(t, files), nil); a != b {
			t.Errorf("two identical trees digest to %q and %q, want one key so an unchanged build is not re-uploaded", a, b)
		}
	})

	t.Run("changed contents digest apart", func(t *testing.T) {
		base := digested(t, builtTree(t, map[string]string{"a.js": "one"}), nil)
		if changed := digested(t, builtTree(t, map[string]string{"a.js": "two"}), nil); base == changed {
			t.Error("the digest ignored a file's contents, so a function would keep serving the code it replaced")
		}
	})

	t.Run("renamed files digest apart", func(t *testing.T) {
		base := digested(t, builtTree(t, map[string]string{"a.js": "one"}), nil)
		if changed := digested(t, builtTree(t, map[string]string{"b.js": "one"}), nil); base == changed {
			t.Error("the digest ignored a rename")
		}
	})

	t.Run("a symlink's target digests apart", func(t *testing.T) {
		linked := func(target string) string {
			dir := builtTree(t, map[string]string{"a.js": "x", "b.js": "x"})
			if err := os.Symlink(target, filepath.Join(dir, "link.js")); err != nil {
				t.Fatal(err)
			}
			return digested(t, dir, nil)
		}
		if linked("a.js") == linked("b.js") {
			t.Error("the digest ignored the symlink target")
		}
	})

	t.Run("the overlay digests with the tree", func(t *testing.T) {
		dir := builtTree(t, files)
		bare, with := digested(t, dir, nil), digested(t, dir, sealed)
		if bare == with {
			t.Error("the digest ignored the overlay, so a resealed package would land on the key the old one holds")
		}
		if again := digested(t, dir, sealed); again != with {
			t.Errorf("one tree and one overlay digest to %q then %q, want one key", with, again)
		}
		other := digested(t, dir, map[string][]byte{".ocel/vars.sealed": []byte("two")})
		if other == with {
			t.Error("the digest ignored the overlay's contents")
		}
	})
}

func packed(t *testing.T, dir string, overlay map[string][]byte) *zip.Reader {
	t.Helper()
	path, err := packArtifact(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("packArtifact: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		t.Fatalf("the packed artifact is not a zip: %v", err)
	}
	return archive
}

func TestAPackedArtifactCarriesTheTreeTheOverlayAndItsSymlinks(t *testing.T) {
	t.Run("the tree and the overlay round trip", func(t *testing.T) {
		dir := builtTree(t, map[string]string{"src/server.js": "handler", "package.json": "{}"})
		files := map[string]string{}
		for _, entry := range packed(t, dir, map[string][]byte{".ocel/vars.sealed": []byte("sealed")}).File {
			body, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name] = string(content)
		}
		want := map[string]string{
			"src/server.js":     "handler",
			"package.json":      "{}",
			".ocel/vars.sealed": "sealed",
		}
		for name, body := range want {
			if files[name] != body {
				t.Errorf("the package holds %s = %q, want %q", name, files[name], body)
			}
		}
		if len(files) != len(want) {
			t.Errorf("the package holds %v, want exactly %v", files, want)
		}
	})

	t.Run("a symlink stays a symlink", func(t *testing.T) {
		dir := builtTree(t, map[string]string{"real.js": "module.exports={}"})
		if err := os.Symlink("real.js", filepath.Join(dir, "link.js")); err != nil {
			t.Fatal(err)
		}
		entries := map[string]*zip.File{}
		for _, entry := range packed(t, dir, nil).File {
			entries[entry.Name] = entry
		}
		link, held := entries["link.js"]
		if !held {
			t.Fatal("the package dropped the symlink")
		}
		if link.Mode()&os.ModeSymlink == 0 {
			t.Errorf("link.js packed as %v, want a symlink: a resolved copy doubles the package a function boots from", link.Mode())
		}
		body, err := link.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		target, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		if string(target) != "real.js" {
			t.Errorf("the symlink points at %q, want %q", target, "real.js")
		}
		if entries["real.js"].Mode()&os.ModeSymlink != 0 {
			t.Error("real.js packed as a symlink, want the regular file it is")
		}
	})
}
