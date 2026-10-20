package functioncode

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
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
	rels, err := images.ArtifactFiles(dir)
	if err != nil {
		t.Fatalf("artifactFiles: %v", err)
	}
	return rels
}

func digested(t *testing.T, dir string, overlay map[string][]byte) string {
	t.Helper()
	sum, err := digest(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return sum
}

func TestAnArtifactDigestNamesOneTreeAndOneOverlay(t *testing.T) {
	files := map[string]string{"src/server.js": "handler", "package.json": `{"name":"app"}`}
	sealed := map[string][]byte{buildoutput.StateDir + "/variables.sealed": []byte("one")}

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
			t.Error("the digest ignored the overlay, so a resealed package would land on the key the old one is stored under")
		}
		if again := digested(t, dir, sealed); again != with {
			t.Errorf("one tree and one overlay digest to %q then %q, want one key", with, again)
		}
		other := digested(t, dir, map[string][]byte{buildoutput.StateDir + "/variables.sealed": []byte("two")})
		if other == with {
			t.Error("the digest ignored the overlay's contents")
		}
	})
}

func packed(t *testing.T, dir string, overlay map[string][]byte) *zip.Reader {
	t.Helper()
	path, err := pack(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("pack: %v", err)
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

func TestAPackedArtifactContainsTheTreeTheOverlayAndItsSymlinks(t *testing.T) {
	t.Run("the tree and the overlay round trip", func(t *testing.T) {
		dir := builtTree(t, map[string]string{"src/server.js": "handler", "package.json": "{}"})
		files := map[string]string{}
		for _, entry := range packed(t, dir, map[string][]byte{buildoutput.StateDir + "/variables.sealed": []byte("sealed")}).File {
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
			"src/server.js": "handler",
			"package.json":  "{}",
			buildoutput.StateDir + "/variables.sealed": "sealed",
		}
		for name, body := range want {
			if files[name] != body {
				t.Errorf("the package contains %s = %q, want %q", name, files[name], body)
			}
		}
		if len(files) != len(want) {
			t.Errorf("the package contains %v, want exactly %v", files, want)
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
		link, ok := entries["link.js"]
		if !ok {
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

func TestStageShipsEveryFunctionWithTheVendorsOverlayAndItsOwn(t *testing.T) {
	sealed := map[string][]byte{buildoutput.StateDir + "/variables.sealed": []byte("sealed for web")}
	spec := provider.StackSpec{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))},
		App: &provider.AppSpec{App: "web", Functions: []provider.FunctionSpec{
			{Name: "server", ArtifactDir: builtTree(t, map[string]string{"index.js": "server"}), Overlay: map[string][]byte{"route-table.json": []byte("{}")}},
			{Name: "feed", ArtifactDir: builtTree(t, map[string]string{"index.js": "feed"})},
		}},
	}
	staged, err := Stage(spec, sealed)
	if err != nil {
		t.Fatalf("Stage() = %v", err)
	}
	t.Cleanup(func() { Discard(staged) })
	if len(staged) != 2 || staged[0].Name != "server" || staged[1].Name != "feed" {
		t.Fatalf("Stage() = %+v, want one upload per function in the app's order", staged)
	}
	for _, upload := range staged {
		files := unzipped(t, upload.Path)
		if files[buildoutput.StateDir+"/variables.sealed"] != "sealed for web" {
			t.Errorf("%s's package holds %v, want the vendor's sealed values in every function: without them the function boots with no variables", upload.Name, files)
		}
		if kind, err := naming.ParseObjectKind(upload.Ref.Key); upload.Ref.Tier != environment.TierProduction || err != nil || kind != naming.ObjectFunction {
			t.Errorf("%s ships to %+v, want the tier's function artifacts under a key its digest names", upload.Name, upload.Ref)
		}
	}
	if _, ok := unzipped(t, staged[0].Path)["route-table.json"]; !ok {
		t.Error("the root function's package dropped the route table its spec handed it")
	}
	if _, ok := unzipped(t, staged[1].Path)["route-table.json"]; ok {
		t.Error("a function whose spec names no route table carries one")
	}
}

func TestAFunctionNamingNoBuildArtifactIsRefused(t *testing.T) {
	spec := provider.StackSpec{App: &provider.AppSpec{App: "web", Functions: []provider.FunctionSpec{{Name: "server"}}}}
	if _, err := Stage(spec, nil); err == nil {
		t.Fatal("Stage() of a function with no build artifact = nil, want it refused: there is nothing to ship")
	}
}

func unzipped(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		t.Fatalf("the staged artifact is not a zip: %v", err)
	}
	files := map[string]string{}
	for _, entry := range archive.File {
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
	return files
}

func TestAFunctionsRepositoryIsANameARegistryTakes(t *testing.T) {
	if got := functionRepository("web", "fn--web--index"); got != "web-index" {
		t.Errorf("functionRepository() = %q, want %q: a registry refuses a path component that repeats a separator", got, "web-index")
	}
	if only := functionRepository("web", "web"); only != "web" {
		t.Errorf("functionRepository() = %q, want the app itself where the function is the whole app", only)
	}
}
