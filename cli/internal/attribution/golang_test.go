package attribution

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/pkg/constants"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/web\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "server", "main.go"), "package main\n\nimport _ \"example.com/web/"+constants.DefaultDiscoveryDirName+"\"\n\nfunc main() {}\n")
	write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "declarations.go"), "package "+constants.DefaultDiscoveryDirName+"\n")
	write(t, filepath.Join(root, "unused", "unused.go"), "package unused\n")
	return root
}

func TestGoReachGrantsAResourceTheAppsMainImports(t *testing.T) {
	root := goApp(t)
	apps := []App{{Name: "web", Path: "server", Language: discovery.Go}}
	declarations := []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, constants.DefaultDiscoveryDirName, "declarations.go") + ":1",
	}}

	usages, err := Compute(t.Context(), root, apps, declarations)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	want := []Usage{{App: "web", Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main", Files: []string{"server"}}}
	if !slices.EqualFunc(usages, want, func(a, b Usage) bool {
		return a.App == b.App && a.Type == b.Type && a.Name == b.Name && slices.Equal(a.Files, b.Files)
	}) {
		t.Errorf("usages = %+v, want %+v", usages, want)
	}
}

func TestGoReachGrantsNothingFromAPackageNoMainImports(t *testing.T) {
	root := goApp(t)
	apps := []App{{Name: "web", Path: "server", Language: discovery.Go}}
	declarations := []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "unused", "unused.go") + ":1",
	}}

	usages, err := Compute(t.Context(), root, apps, declarations)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 0 {
		t.Errorf("usages = %+v, want none", usages)
	}
}

func TestGoReachReportsWhatGoListSaid(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/web\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "server", "main.go"), "package main\n\nimport _ \"example.com/web/missing\"\n\nfunc main() {}\n")

	declarations := []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "server", "main.go") + ":1",
	}}

	_, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Go}}, declarations)
	if err == nil {
		t.Fatal("Compute succeeded on a module that does not build, want error")
	}
	if !strings.Contains(err.Error(), `attribution: app "web": go list:`) {
		t.Errorf("error = %q, want it to name the app and go list", err)
	}
}

func TestGoReachGrantsTheFixtureResourceToItsApp(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "sdk", "go"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}

	usages, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Go}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, constants.DefaultDiscoveryDirName, "infra.go") + ":5",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("usages = %+v, want one", usages)
	}
	if usages[0].App != "web" || usages[0].Name != "main" || !slices.Equal(usages[0].Files, []string{"server"}) {
		t.Errorf("usage = %+v, want main granted to web from entry server", usages[0])
	}
}

func TestGoReachStopsAtTheModuleTheAppLivesIn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.work"), "go 1.27.0\n\nuse (\n\t./server\n\t./shared\n)\n")
	write(t, filepath.Join(root, "server", "go.mod"), "module example.com/web\n\ngo 1.27.0\n\nrequire example.com/shared v0.0.0\n")
	write(t, filepath.Join(root, "server", "main.go"), "package main\n\nimport _ \"example.com/shared/"+constants.DefaultDiscoveryDirName+"\"\n\nfunc main() {}\n")
	write(t, filepath.Join(root, "shared", "go.mod"), "module example.com/shared\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "shared", constants.DefaultDiscoveryDirName, "declarations.go"), "package "+constants.DefaultDiscoveryDirName+"\n")

	usages, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Go}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "shared", constants.DefaultDiscoveryDirName, "declarations.go") + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 0 {
		t.Errorf("usages = %+v, want none: the declaration lives outside the app's module", usages)
	}
}
