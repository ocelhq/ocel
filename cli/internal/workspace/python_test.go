package workspace_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func TestAPythonAppStartsItsResolvedEntrypointThroughTheServerProgramItsBuildWrites(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "main.py"), []byte("app = None\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for entrypoint, start := range map[string]string{
		"main.py":      "cd api && python3 ocel-server.py file main.py",
		"shop.api:app": "cd api && python3 ocel-server.py module shop.api:app",
	} {
		loc := workspace.Location{Root: root, Path: "api", Language: language.Python, Entrypoint: entrypoint}
		commands, err := loc.Commands()
		if err != nil {
			t.Fatalf("Commands(%q) = %v", entrypoint, err)
		}
		want := workspace.Commands{Start: start, Asset: workspace.Asset{Name: pythonserver.File, Content: pythonserver.Source}}
		if !reflect.DeepEqual(commands, want) {
			t.Errorf("Commands(%q) starts %q writing %q, want %q writing the server program", entrypoint, commands.Start, commands.Asset.Name, start)
		}
	}
}

func TestAPythonAppWithNoEntrypointIsLeftToRailpacksOwnStartCommand(t *testing.T) {
	loc := workspace.Location{Root: t.TempDir(), Path: ".", Language: language.Python}

	commands, err := loc.Commands()
	if err != nil || !reflect.DeepEqual(commands, workspace.Commands{}) {
		t.Errorf("Commands() = %+v, %v, want nothing: with no entrypoint resolved, railpack decides how the image starts, as it does for a Django project", commands, err)
	}
}

func TestAPythonAppInADirectoryWithASpaceStartsInThatDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "my api", "main.py"), []byte("app = None\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loc := workspace.Location{Root: root, Path: "my api", Language: language.Python, Entrypoint: "main.py"}

	commands, err := loc.Commands()
	if want := "cd 'my api' && python3 ocel-server.py file main.py"; err != nil || commands.Start != want {
		t.Errorf("Commands() = %+v, %v, want the start %q", commands, err, want)
	}
}

func TestAPythonAppIsBuiltFromTheDirectoryHoldingItAndEveryPackageItsUvSourcesNameByPath(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		root  string
		path  string
	}{
		{
			name: "a path source",
			files: map[string]string{
				"tests/fixtures/web/pyproject.toml": "[project]\nname = \"web\"\ndependencies = [\"ocel\"]\n\n[tool.uv.sources]\nocel = { path = \"../../../python/ocel\", editable = true }\n",
				"python/ocel/pyproject.toml":        "[project]\nname = \"ocel\"\n",
			},
			root: ".",
			path: "tests/fixtures/web",
		},
		{
			name: "a path source of a path source, among sources split by marker",
			files: map[string]string{
				"tests/fixtures/web/pyproject.toml": "[project]\nname = \"web\"\n\n[tool.uv.sources]\nlib = [\n  { path = \"../lib\", marker = \"sys_platform == 'linux'\" },\n  { index = \"internal\", marker = \"sys_platform != 'linux'\" },\n]\n",
				"tests/fixtures/lib/pyproject.toml": "[project]\nname = \"lib\"\n\n[tool.uv.sources]\nshared = { path = \"../../../vendor/shared\" }\n",
				"vendor/shared/pyproject.toml":      "[project]\nname = \"shared\"\n",
			},
			root: ".",
			path: "tests/fixtures/web",
		},
		{
			name: "a wheel named as a path source",
			files: map[string]string{
				"tests/fixtures/web/pyproject.toml":              "[project]\nname = \"web\"\n\n[tool.uv.sources]\nfoo = { path = \"../wheels/foo-1.0-py3-none-any.whl\" }\n",
				"tests/fixtures/wheels/foo-1.0-py3-none-any.whl": "",
			},
			root: "tests/fixtures",
			path: "web",
		},
		{
			name: "only index, git and workspace sources",
			files: map[string]string{
				"tests/fixtures/web/pyproject.toml": "[project]\nname = \"web\"\n\n[tool.uv.sources]\ntorch = { index = \"pytorch\" }\nfork = { git = \"https://example.com/fork.git\" }\nmember = { workspace = true }\n",
			},
			root: "tests/fixtures/web",
			path: ".",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files[".git/HEAD"] = "ref: refs/heads/main\n"
			write(t, dir, tt.files)

			got := locatedIn(t, dir, filepath.Join(dir, "tests", "fixtures", "web"), language.Python)

			if want := filepath.Join(dir, filepath.FromSlash(tt.root)); got.Root != want || got.Path != tt.path {
				t.Errorf("Locate() = %+v, want the context %s with the app at %s — uv sync in the image reads every package its sources name by path", got, want, tt.path)
			}
		})
	}
}

func TestAPythonAppWhoseUvSourceLiesOutsideTheRepositoryIsRefusedRatherThanBuiltFromAboveIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"elsewhere/lib/pyproject.toml": "[project]\nname = \"lib\"\n",
		"repo/.git/HEAD":               "ref: refs/heads/main\n",
		"repo/web/pyproject.toml":      "[project]\nname = \"web\"\n\n[tool.uv.sources]\nlib = { path = \"../../elsewhere/lib\" }\n",
	})

	_, err := locate(t, filepath.Join(dir, "repo"), filepath.Join(dir, "repo", "web"), language.Python)
	if err == nil {
		t.Fatal("Locate() took a context above the repository the app is checked out in, so the image would be built from whatever else sits beside it")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "elsewhere", "lib")) || !strings.Contains(err.Error(), filepath.Join(dir, "repo")) {
		t.Errorf("Locate() = %v, and the reader is never told which package lies outside which repository", err)
	}
}

func TestAUvProjectBelowItsContextIsSyncedInItsOwnDirectoryAndStartedWithThePythonItSynced(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                  "ref: refs/heads/main\n",
		"python/ocel/pyproject.toml": "[project]\nname = \"ocel\"\n",
		"apps/web/pyproject.toml":    "[project]\nname = \"web\"\n\n[tool.uv.sources]\nocel = { path = \"../../python/ocel\" }\n",
		"apps/web/uv.lock":           "version = 1\n",
		"apps/web/main.py":           "print('hi')\n",
	})

	commands, err := locatedWith(t, dir, filepath.Join(dir, "apps", "web"), language.Python, "main.py").Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	want := workspace.Commands{
		Install:         "cd apps/web && UV_PYTHON_DOWNLOADS=never uv sync --locked --no-dev --no-editable",
		Start:           "cd apps/web && .venv/bin/python3 ocel-server.py file main.py",
		Asset:           workspace.Asset{Name: pythonserver.File, Content: pythonserver.Source},
		Tools:           []workspace.Tool{{Name: "uv", Version: "latest"}},
		InstallsInBuild: true,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("Commands() = %+v, want %+v — no pyproject.toml sits at the context root for railpack to sync, so the build step syncs the app's own", commands, want)
	}
}

func TestAUvProjectBelowAContextRootWithItsOwnPyprojectIsStillSyncedInItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                "ref: refs/heads/main\n",
		"pyproject.toml":           "[tool.ruff]\nline-length = 100\n",
		"libs/ocel/pyproject.toml": "[project]\nname = \"ocel\"\n",
		"apps/web/pyproject.toml":  "[project]\nname = \"web\"\n\n[tool.uv.sources]\nocel = { path = \"../../libs/ocel\" }\n",
		"apps/web/uv.lock":         "version = 1\n",
		"apps/web/main.py":         "print('hi')\n",
	})

	commands, err := locatedWith(t, dir, filepath.Join(dir, "apps", "web"), language.Python, "main.py").Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	if want := "cd apps/web && UV_PYTHON_DOWNLOADS=never uv sync --locked --no-dev --no-editable"; commands.Install != want || !commands.InstallsInBuild {
		t.Errorf("Commands() = %+v, want the install %q run in the build step — a pyproject.toml at the context root that only configures tools installs nothing the app needs", commands, want)
	}
}

func TestAUvProjectBelowItsContextInstallsThePythonItsPythonVersionFileNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                  "ref: refs/heads/main\n",
		"python/ocel/pyproject.toml": "[project]\nname = \"ocel\"\n",
		"apps/web/pyproject.toml":    "[project]\nname = \"web\"\n\n[tool.uv.sources]\nocel = { path = \"../../python/ocel\" }\n",
		"apps/web/uv.lock":           "version = 1\n",
		"apps/web/.python-version":   "3.12\n",
		"apps/web/main.py":           "print('hi')\n",
	})

	commands, err := locatedWith(t, dir, filepath.Join(dir, "apps", "web"), language.Python, "main.py").Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	if want := (workspace.Tool{Name: "python", Version: "3.12"}); !slices.Contains(commands.Tools, want) {
		t.Errorf("Commands().Tools = %+v, want %+v — railpack reads .python-version only at the context root, and uv sync downloads no other python", commands.Tools, want)
	}
}

func TestAUvProjectBelowItsContextRunsTheBuildCommandItNamesAfterItsSync(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                  "ref: refs/heads/main\n",
		"python/ocel/pyproject.toml": "[project]\nname = \"ocel\"\n",
		"apps/web/pyproject.toml":    "[project]\nname = \"web\"\n\n[tool.uv.sources]\nocel = { path = \"../../python/ocel\" }\n",
		"apps/web/uv.lock":           "version = 1\n",
		"apps/web/main.py":           "print('hi')\n",
	})
	loc := locatedWith(t, dir, filepath.Join(dir, "apps", "web"), language.Python, "main.py")
	loc.BuildCommand = "python -c 1"

	commands, err := loc.Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	if commands.Build != "python -c 1" || !strings.Contains(commands.Install, "uv sync") || !commands.InstallsInBuild {
		t.Errorf("Commands() = %+v, want the sync kept as the install the build step runs, and the build command run after it", commands)
	}
}
