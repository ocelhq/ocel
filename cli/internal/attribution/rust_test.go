package attribution

import (
	"os/exec"
	"path/filepath"
	"slices"
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

func TestRustReachGrantsTheFixtureResourceToItsApp(t *testing.T) {
	needsCargo(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "sdk", "rust"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}
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
