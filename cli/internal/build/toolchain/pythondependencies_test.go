package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func exported(t *testing.T, source string) (pythonDependencies, string) {
	t.Helper()
	c := Compilation{App: "web", Framework: buildoutput.Framework{Name: "python", Arch: "x86_64"}, Source: source}
	deps, err := c.exportDependencies(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("export the dependencies of %s: %v", source, err)
	}
	if deps.requirements == "" {
		return deps, ""
	}
	return deps, readFile(t, deps.requirements)
}

func refusedExport(t *testing.T, source string) error {
	t.Helper()
	c := Compilation{App: "web", Framework: buildoutput.Framework{Name: "python", Arch: "x86_64"}, Source: source}
	_, err := c.exportDependencies(context.Background(), t.TempDir())
	return err
}

func fakeUv(t *testing.T, writes string) string {
	t.Helper()
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + argv + `
while [ $# -gt 0 ]; do
  if [ "$1" = "--output-file" ]; then printf '` + writes + `' > "$2"; fi
  shift
done
`
	if err := os.WriteFile(filepath.Join(bin, "uv"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}

func TestAPythonAppsRequirementsFileIsVendoredBeforeAnythingElseItDeclares(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":          "print('hi')\n",
		"requirements.txt": "six==1.17.0\n",
		"pyproject.toml":   "[project]\nname = \"web\"\ndependencies = [\"flask\"]\n",
		"Pipfile.lock":     `{"default": {"django": {"version": "==5.0"}}}`,
	})
	deps, requirements := exported(t, source)

	if deps.declaredIn != pythonRequirementsFile || requirements != "six==1.17.0\n" {
		t.Errorf("vendored %q from %s, want requirements.txt as written", requirements, deps.declaredIn)
	}
}

func TestAPythonAppsUvLockIsExportedAsTheRequirementsPipVendors(t *testing.T) {
	argv := fakeUv(t, `six==1.17.0\n`)
	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[project]\nname = \"web\"\ndependencies = [\"six\"]\n",
		"uv.lock":        "version = 1\n",
	})
	deps, requirements := exported(t, source)

	if deps.declaredIn != pythonUvLockFile || requirements != "six==1.17.0\n" {
		t.Errorf("vendored %q from %s, want what uv exported from uv.lock", requirements, deps.declaredIn)
	}
	said := strings.Fields(readFile(t, argv))
	for _, want := range []string{"export", "--frozen", "--format", "requirements-txt", "--no-dev", "--no-editable", "--no-hashes"} {
		if !strings.Contains(" "+strings.Join(said, " ")+" ", " "+want+" ") {
			t.Errorf("uv ran as %q, with no %s: the lock is exported as it stands, without dev dependencies, as files pip installs into the artifact", said, want)
		}
	}
}

func TestAPythonAppWithAUvLockAndNoUvIsRefusedNamingUv(t *testing.T) {
	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[project]\nname = \"web\"\ndependencies = [\"six\"]\n",
		"uv.lock":        "version = 1\n",
	})
	t.Setenv("PATH", "")

	err := refusedExport(t, source)
	if err == nil || !strings.Contains(err.Error(), "uv ") || !strings.Contains(err.Error(), pythonUvLockFile) {
		t.Fatalf("err = %v, want a refusal naming uv and uv.lock — vendoring [project].dependencies instead would ship versions the lock never pinned", err)
	}
}

func TestAPythonAppsProjectDependenciesAreVendoredWhenNothingLocksThem(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[project]\nname = \"web\"\ndependencies = [\"flask>=3\", \"requests[socks]==2.32.3 ; python_version >= '3.9'\"]\n",
	})
	deps, requirements := exported(t, source)

	if want := "flask>=3\nrequests[socks]==2.32.3 ; python_version >= '3.9'\n"; deps.declaredIn != pythonProjectFile || requirements != want {
		t.Errorf("vendored %q from %s, want %q from pyproject.toml", requirements, deps.declaredIn, want)
	}
}

func TestAPythonAppWhosePyprojectDeclaresNoDependenciesVendorsNothing(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[project]\nname = \"web\"\n",
	})
	deps, requirements := exported(t, source)

	if requirements != "" {
		t.Errorf("vendored %q from %s, want nothing", requirements, deps.declaredIn)
	}
}

func TestAPythonAppDeclaringItsDependenciesForPoetryAloneIsRefused(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[tool.poetry]\nname = \"web\"\n\n[tool.poetry.dependencies]\npython = \"^3.12\"\nflask = \"^3.0\"\n",
	})

	err := refusedExport(t, source)
	if err == nil || !strings.Contains(err.Error(), "tool.poetry.dependencies") {
		t.Fatalf("err = %v, want a refusal naming tool.poetry.dependencies — the artifact would ship without them and fail at the first import", err)
	}
}

func TestAPyprojectHoldingOnlyToolConfigurationLeavesThePipfileLockToBeVendored(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":        "print('hi')\n",
		"pyproject.toml": "[tool.ruff]\nline-length = 120\n",
		"Pipfile.lock":   `{"_meta": {}, "default": {"six": {"version": "==1.17.0"}}, "develop": {"pytest": {"version": "==8.0.0"}}}`,
	})
	deps, requirements := exported(t, source)

	if deps.declaredIn != pythonPipfileLock || requirements != "six==1.17.0\n" {
		t.Errorf("vendored %q from %s, want the default packages of Pipfile.lock and none of its develop ones", requirements, deps.declaredIn)
	}
}

func TestAPipfileLocksDefaultPackagesAreVendoredWithTheirExtrasMarkersAndSources(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py": "print('hi')\n",
		"Pipfile.lock": `{"default": {
			"requests": {"version": "==2.32.3", "extras": ["socks"], "markers": "python_version >= '3.9'", "hashes": ["sha256:abc"]},
			"shared": {"path": "../shared", "editable": true},
			"tool": {"git": "https://github.com/acme/tool.git", "ref": "0123abc"},
			"wheel": {"file": "https://example.com/wheel-1.0-py3-none-any.whl"},
			"anything": {"version": "*"}
		}}`,
	})
	_, requirements := exported(t, source)

	want := strings.Join([]string{
		"anything",
		"requests[socks]==2.32.3 ; python_version >= '3.9'",
		"../shared",
		"tool @ git+https://github.com/acme/tool.git@0123abc",
		"wheel @ https://example.com/wheel-1.0-py3-none-any.whl",
	}, "\n") + "\n"
	if requirements != want {
		t.Errorf("vendored\n%s\nwant\n%s", requirements, want)
	}
}

func TestAPythonAppWithAPipfileAndNoLockIsRefused(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py": "print('hi')\n",
		"Pipfile": "[packages]\nflask = \"*\"\n",
	})

	err := refusedExport(t, source)
	if err == nil || !strings.Contains(err.Error(), pythonPipfileLock) {
		t.Fatalf("err = %v, want a refusal naming Pipfile.lock — an unlocked Pipfile names no versions to vendor", err)
	}
}

func TestAPythonAppDeclaringNoDependenciesVendorsNothing(t *testing.T) {
	t.Parallel()

	deps, requirements := exported(t, pythonApp(t, map[string]string{"main.py": "print('hi')\n"}))

	if requirements != "" || deps.declaredIn != "" {
		t.Errorf("vendored %q from %q, want nothing", requirements, deps.declaredIn)
	}
}
