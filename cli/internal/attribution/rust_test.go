package attribution

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func needsCargo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}
}

func rustApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "Cargo.toml"), "[workspace]\nmembers = [\"app\", \"shared\", \"unused\"]\nresolver = \"2\"\n")
	write(t, filepath.Join(root, "app", "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nshared = { path = \"../shared\" }\n")
	write(t, filepath.Join(root, "app", "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(root, "app", "infra", "mod.rs"), "pub const NAME: &str = \"main\";\n")
	write(t, filepath.Join(root, "shared", "Cargo.toml"), "[package]\nname = \"shared\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	write(t, filepath.Join(root, "shared", "src", "lib.rs"), "")
	write(t, filepath.Join(root, "unused", "Cargo.toml"), "[package]\nname = \"unused\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	write(t, filepath.Join(root, "unused", "src", "lib.rs"), "")
	return root
}

func rustUsages(t *testing.T, root, source string, roots []discovery.Root) []Usage {
	t.Helper()
	app := App{Name: "web", Path: "app", Language: discovery.Rust, Roots: roots}
	usages, err := Compute(t.Context(), root, []App{app}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: source + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return usages
}

func TestRustReachGrantsAResourceTheCrateDeclares(t *testing.T) {
	needsCargo(t)
	root := rustApp(t)
	usages := rustUsages(t, root, filepath.Join(root, "app", "infra", "mod.rs"), nil)

	want := []Usage{{App: "web", Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main", Files: []string{"app/src/main.rs"}}}
	if !slices.EqualFunc(usages, want, func(a, b Usage) bool {
		return a.App == b.App && a.Type == b.Type && a.Name == b.Name && slices.Equal(a.Files, b.Files)
	}) {
		t.Errorf("usages = %+v, want %+v", usages, want)
	}
}

func TestRustReachGrantsAResourceAWorkspaceDependencyDeclares(t *testing.T) {
	needsCargo(t)
	root := rustApp(t)
	usages := rustUsages(t, root, filepath.Join(root, "shared", "src", "lib.rs"), nil)
	if len(usages) != 1 || !slices.Equal(usages[0].Files, []string{"app/src/main.rs"}) {
		t.Errorf("usages = %+v, want main granted to web from entry app/src/main.rs", usages)
	}
}

func TestRustReachGrantsNothingFromACrateTheAppDoesNotDependOn(t *testing.T) {
	needsCargo(t)
	root := rustApp(t)
	usages := rustUsages(t, root, filepath.Join(root, "unused", "src", "lib.rs"), nil)
	if len(usages) != 0 {
		t.Errorf("usages = %+v, want none", usages)
	}
}

func TestRustReachSearchesTheDiscoveryPathsTheProjectConfigures(t *testing.T) {
	needsCargo(t)
	root := rustApp(t)
	write(t, filepath.Join(root, "decls", "mod.rs"), "pub const NAME: &str = \"main\";\n")

	roots, err := discovery.Roots(root, []string{"decls"})
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	usages := rustUsages(t, root, filepath.Join(root, "decls", "mod.rs"), roots)
	if len(usages) != 1 || !slices.Equal(usages[0].Files, []string{"app/src/main.rs"}) {
		t.Errorf("usages = %+v, want main granted to web from entry app/src/main.rs", usages)
	}
}

func fetched(t *testing.T, crate string) {
	t.Helper()
	fetch := exec.Command("cargo", "fetch", "--quiet")
	fetch.Dir = crate
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Fatalf("cargo fetch in %s: %v\n%s", crate, err, out)
	}
}

func TestRustReachGrantsTheFixtureResourceToItsApp(t *testing.T) {
	needsCargo(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "sdk", "rust"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}
	fetched(t, root)
	roots, err := discovery.Roots(root, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}

	app := App{Name: "web", Path: ".", Language: discovery.Rust, Roots: roots}
	usages, err := Compute(t.Context(), root, []App{app}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "infra", "mod.rs") + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("usages = %+v, want one", usages)
	}
	if usages[0].App != "web" || usages[0].Name != "main" || !slices.Equal(usages[0].Files, []string{"src/main.rs"}) {
		t.Errorf("usage = %+v, want main granted to web from entry src/main.rs", usages[0])
	}
}

func TestRustReachReadsCargoMetadataWithoutReachingTheRegistry(t *testing.T) {
	bin := t.TempDir()
	recorded := filepath.Join(bin, "args")
	fake := filepath.Join(bin, "cargo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+recorded+"\necho '{\"packages\":[],\"workspace_members\":[],\"workspace_root\":\"\"}'\n"), 0o755); err != nil {
		t.Fatalf("write the fake cargo: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	write(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	_, _ = rustReach{}.Entries(t.Context(), root, App{Name: "web", Path: ".", Language: discovery.Rust})

	args, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatalf("read what cargo was called with: %v", err)
	}
	if !slices.Contains(strings.Fields(string(args)), "--offline") {
		t.Errorf("cargo metadata args = %q, want --offline among them", args)
	}
}

func TestRustReachRefusesAnAppThatBuildsSeveralBinaries(t *testing.T) {
	needsCargo(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(root, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(root, "src", "worker.rs"), "fn main() {}\n")

	_, err := rustReach{}.Entries(t.Context(), root, App{Name: "web", Path: ".", Language: discovery.Rust})
	if err == nil {
		t.Fatal("Entries succeeded on an app with two binaries, want an error")
	}
	want := `attribution: app "web" builds 2 binaries, and ocel attributes one binary per app`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}
