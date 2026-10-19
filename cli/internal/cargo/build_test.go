package cargo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const webID = "path+file:///work/web#0.1.0"

func TestBuildFindsTheExecutableOfItsBinaryAmongWhatCargoSaid(t *testing.T) {
	said := strings.Join([]string{
		`warning: a line a build script printed`,
		`{"reason":"compiler-artifact","package_id":"path+file:///work/web#0.1.0","target":{"name":"web","kind":["lib"]},"executable":null}`,
		`{"reason":"compiler-artifact","package_id":"path+file:///work/other#web@0.1.0","target":{"name":"web","kind":["bin"]},"executable":"/work/target/debug/other-web"}`,
		`{"reason":"compiler-artifact","package_id":"path+file:///work/web#0.1.0","target":{"name":"worker","kind":["bin"]},"executable":"/work/target/debug/worker"}`,
		`{"reason":"compiler-artifact","package_id":"path+file:///work/web#0.1.0","target":{"name":"web","kind":["bin"]},"executable":"/work/target/debug/web"}`,
		`{"reason":"build-finished","success":true}`,
	}, "\n")
	build := Build{Crate: Package{ID: webID, Name: "web"}, Bin: "web"}

	executable, err := build.findExecutable(strings.NewReader(said))
	if err != nil || executable != "/work/target/debug/web" {
		t.Fatalf("findExecutable = %q, %v, want the bin web of the crate web", executable, err)
	}
}

func TestBuildRefusesWhenCargoNamedNoExecutableOfItsBinary(t *testing.T) {
	said := `{"reason":"compiler-artifact","package_id":"path+file:///work/web#0.1.0","target":{"name":"web","kind":["lib"]},"executable":null}` + "\n" + `{"reason":"build-finished","success":true}`
	build := Build{Crate: Package{ID: webID, Name: "web"}, Bin: "web"}

	if executable, err := build.findExecutable(strings.NewReader(said)); err == nil || !strings.Contains(err.Error(), `"web"`) {
		t.Fatalf("findExecutable = %q, %v, want a refusal naming the binary web", executable, err)
	}
}

func binCrate(t *testing.T, main string) (Workspace, Package) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := Metadata(context.Background(), dir, "--no-deps")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	crate, ok := workspace.PackageAt(dir)
	if !ok {
		t.Fatalf("no package at %s", dir)
	}
	return workspace, crate
}

func TestBuildCompilesTheBinaryAndNamesItsExecutable(t *testing.T) {
	workspace, crate := binCrate(t, "fn main() {}\n")

	executable, err := Build{Crate: crate, Bin: "web", Dir: workspace.Root}.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if want := filepath.Join(workspace.Target, "debug", "web"); executable != want {
		t.Errorf("executable = %q, want %q", executable, want)
	}
	if _, err := os.Stat(executable); err != nil {
		t.Errorf("the executable was not written: %v", err)
	}
}

func TestBuildRefusesACrateThatDoesNotCompileWithWhatTheCompilerSaid(t *testing.T) {
	workspace, crate := binCrate(t, "fn main() { undefined() }\n")

	_, err := Build{Crate: crate, Bin: "web", Dir: workspace.Root}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "undefined") {
		t.Fatalf("Run = %v, want the compile error in it", err)
	}
}
