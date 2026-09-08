package attribution

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func pythonApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "infra", "__init__.py"), "db = 1\n")
	write(t, filepath.Join(root, "server", "main.py"), "from infra import db\n\nprint(db)\n")
	write(t, filepath.Join(root, "unused", "__init__.py"), "other = 1\n")
	return root
}

func pythonRoots(t *testing.T, root string, paths []string) []discovery.Root {
	t.Helper()
	roots, err := discovery.Roots(root, paths)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	return roots
}

func TestPythonReachGrantsAResourceTheAppsEntryImports(t *testing.T) {
	root := pythonApp(t)
	usages, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Python, Roots: pythonRoots(t, root, nil)}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "infra", "__init__.py") + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	want := []Usage{{App: "web", Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main", Files: []string{"server/main.py"}}}
	if !slices.EqualFunc(usages, want, func(a, b Usage) bool {
		return a.App == b.App && a.Type == b.Type && a.Name == b.Name && slices.Equal(a.Files, b.Files)
	}) {
		t.Errorf("usages = %+v, want %+v", usages, want)
	}
}

func TestPythonReachGrantsNothingFromAModuleNoEntryImports(t *testing.T) {
	root := pythonApp(t)
	usages, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Python, Roots: pythonRoots(t, root, nil)}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "unused", "__init__.py") + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 0 {
		t.Errorf("usages = %+v, want none", usages)
	}
}

func TestPythonReachRefusesAnImportOnlyRunningTheAppWouldResolve(t *testing.T) {
	root := pythonApp(t)
	write(t, filepath.Join(root, "server", "main.py"), "import importlib\n\nname = \"infra\"\nmodule = importlib.import_module(name)\n")

	_, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Python, Roots: pythonRoots(t, root, nil)}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "infra", "__init__.py") + ":1",
	}})
	var unresolved *UnresolvedImportError
	if !errors.As(err, &unresolved) {
		t.Fatalf("Compute err = %v, want an *UnresolvedImportError", err)
	}
	if unresolved.App != "web" || unresolved.File != "server/main.py" || unresolved.Line != 4 {
		t.Errorf("error = %+v, want it to name server/main.py line 4 of web", unresolved)
	}
}

func TestPythonReachGrantsTheFixtureResourceToItsApp(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "sdk", "python"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}

	usages, err := Compute(t.Context(), root, []App{{Name: "web", Path: "server", Language: discovery.Python, Roots: pythonRoots(t, root, nil)}}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "infra", "__init__.py") + ":3",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("usages = %+v, want one", usages)
	}
	if usages[0].App != "web" || usages[0].Name != "main" || !slices.Equal(usages[0].Files, []string{"server/main.py"}) {
		t.Errorf("usage = %+v, want main granted to web from entry server/main.py", usages[0])
	}
}

func TestPythonReachSearchesTheDiscoveryPathsTheProjectConfigures(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "decls", "__init__.py"), "db = 1\n")
	write(t, filepath.Join(root, "server", "main.py"), "from decls import db\n\nprint(db)\n")

	app := App{Name: "web", Path: "server", Language: discovery.Python, Roots: pythonRoots(t, root, []string{"decls"})}
	usages, err := Compute(t.Context(), root, []App{app}, []Declaration{{
		Type:   linksv1.LinkType_LINK_TYPE_POSTGRES,
		Name:   "main",
		Source: filepath.Join(root, "decls", "__init__.py") + ":1",
	}})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(usages) != 1 || !slices.Equal(usages[0].Files, []string{"server/main.py"}) {
		t.Fatalf("usages = %+v, want main granted to web from entry server/main.py", usages)
	}
}
