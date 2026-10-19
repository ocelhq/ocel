package pythonserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
)

func TestAPythonAppWithNoEntrypointIsRefusedNamingEveryCandidate(t *testing.T) {
	t.Parallel()

	_, err := ResolveEntrypoint(appDir(t, map[string]string{"main.py": "print('hi')\n"}), "")
	if err == nil || !strings.Contains(err.Error(), strings.Join(language.PythonEntrypoints, ", ")) || !strings.Contains(err.Error(), `"entrypoint"`) {
		t.Fatalf("err = %v, want a refusal naming every file an app is detected from and the key that names another — the project resolves the entrypoint, and the build serves what it resolved", err)
	}
}

func TestADeclaredPythonModuleEntrypointIsServedAsWritten(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"main.py": "print('hi')\n"})
	for _, declared := range []string{"my_pkg.api:app", "my_pkg.api", "service", "_private.v2:make_app"} {
		got, err := ResolveEntrypoint(source, declared)
		if err != nil || got != (Entrypoint{Module: declared}) {
			t.Errorf("entrypoint %q resolves to %+v (err %v), want it served as a module, as written: a module may be installed rather than in the app's directory", declared, got, err)
		}
	}
}

func TestADeclaredPythonFileEntrypointIsServedFromThatFile(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"main.py": "print('hi')\n", "src/api.py": "app = None\n"})
	for _, declared := range []string{"src/api.py", "./src/api.py"} {
		got, err := ResolveEntrypoint(source, declared)
		if err != nil || got != (Entrypoint{File: "src/api.py"}) {
			t.Errorf("entrypoint %q resolves to %+v (err %v), want the file src/api.py", declared, got, err)
		}
	}
}

func TestAPythonEntrypointNamingNoFileInTheAppIsRefused(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"main.py": "print('hi')\n", "src/pkg/__init__.py": ""})
	for _, declared := range []string{"src/missing.py", "src/pkg.py", "src/pkg/"} {
		_, err := ResolveEntrypoint(source, declared)
		if err == nil || !strings.Contains(err.Error(), declared) {
			t.Errorf("entrypoint %q: err = %v, want a refusal naming it — the server would fail to import it only once deployed", declared, err)
		}
	}
}

func TestAPythonEntrypointPathThatIsNotAPythonFileIsRefused(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"main.py": "print('hi')\n", "bin/serve": "#!/bin/sh\n", "src/main.txt": "hi\n"})
	for _, declared := range []string{"bin/serve", "src/main.txt"} {
		_, err := ResolveEntrypoint(source, declared)
		if err == nil || !strings.Contains(err.Error(), ".py") {
			t.Errorf("entrypoint %q: err = %v, want a refusal saying a path entrypoint is a .py file", declared, err)
		}
	}
}

func TestAPythonEntrypointOutsideTheAppsDirectoryIsRefused(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"app/main.py": "print('hi')\n"})
	for _, declared := range []string{"../main.py", "/srv/main.py", "app/../../main.py"} {
		_, err := ResolveEntrypoint(source+"/app", declared)
		if err == nil || !strings.Contains(err.Error(), declared) {
			t.Errorf("entrypoint %q: err = %v, want a refusal — an artifact holds the app's own directory and nothing above it", declared, err)
		}
	}
}

func TestAPythonEntrypointThatIsNeitherAFileNorAModulePathIsRefused(t *testing.T) {
	t.Parallel()

	source := appDir(t, map[string]string{"main.py": "print('hi')\n"})
	for _, declared := range []string{"my-pkg:app", "pkg:", ":app", "pkg:app:extra", "1pkg", "pkg..api", "pkg.api:app.factory"} {
		_, err := ResolveEntrypoint(source, declared)
		if err == nil || !strings.Contains(err.Error(), "module") {
			t.Errorf("entrypoint %q: err = %v, want a refusal saying what a module entrypoint looks like", declared, err)
		}
	}
}

func appDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
