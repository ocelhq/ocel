package toolchain

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
)

func pythonApp(t *testing.T, files map[string]string) string {
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

func vendored(t *testing.T, source, architecture string) (string, string) {
	t.Helper()
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: architecture},
		Source:      source,
		Entrypoint:  "main.py",
		FunctionDir: functionDir,
		AppDir:      appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return appDir, functionDir
}

func TestCompileRefusesAPythonAppDirectoryHoldingNoEntrypoint(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"lib/main.py": "print('hi')\n"})
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), language.PythonEntrypoints[0]) {
		t.Fatalf("err = %v, want a refusal naming %s and the files an app is served from — nothing else in the directory says which module serves the app", err, source)
	}
}

func TestCompileCopiesThePythonAppsOwnSourceIntoTheArtifact(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":              "print('hi')\n",
		"probes.py":            "answer = 42\n",
		"static/ocel.svg":      "<svg/>\n",
		"__pycache__/main.pyc": "stale bytecode\n",
	})
	_, functionDir := vendored(t, source, "x86_64")

	for _, rel := range []string{"main.py", "probes.py", filepath.Join("static", "ocel.svg")} {
		if _, err := os.Stat(filepath.Join(functionDir, rel)); err != nil {
			t.Errorf("the artifact contains no %s: a python app is served from the files it was written as, not from a bundle: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(functionDir, "__pycache__")); err == nil {
		t.Error("the artifact contains __pycache__, whose bytecode was compiled by whatever python built the app rather than the one that runs it")
	}
}

func TestCompileDeclaresTheServerProgramAPythonArtifactIsServedBy(t *testing.T) {
	t.Parallel()

	appDir, functionDir := vendored(t, pythonApp(t, map[string]string{"main.py": "print('hi')\n"}), "arm64")

	var config buildoutput.FunctionConfig
	readJSON(t, filepath.Join(functionDir, buildoutput.FunctionConfigFile), &config)
	if config.EntryFile != pythonserver.File {
		t.Errorf("entryFile = %q, want %q — a host refuses a package whose entry names no file in it", config.EntryFile, pythonserver.File)
	}
	if want := []string{containerimage.PythonWorkerEntry.Interpreter, pythonserver.File, "file", "main.py"}; !slices.Equal(config.Command, want) {
		t.Errorf("command = %q, want %q: the server program serves the entrypoint it is handed, as the file it resolved to", config.Command, want)
	}
	if served := readFile(t, filepath.Join(functionDir, pythonserver.File)); served != pythonserver.Source {
		t.Errorf("the artifact's %s is not the server program ocel ships", pythonserver.File)
	}
	if config.Framework != (buildoutput.Framework{Name: "python", Arch: "arm64"}) {
		t.Errorf("runtime = %+v, want the python runtime at the architecture it was vendored for", config.Framework)
	}
	if config.App != "web" {
		t.Errorf("app = %q, want %q", config.App, "web")
	}

	var hosting buildoutput.Hosting
	readJSON(t, filepath.Join(appDir, buildoutput.HostingFile), &hosting)
	if hosting.Framework != "python" {
		t.Errorf("hosting.json names runtime %q, want %q", hosting.Framework, "python")
	}
	if hosting.FrameworkBuildID == "" {
		t.Error("hosting.json names no build id, and a release is identified by one")
	}
	if len(hosting.Needs) != 0 {
		t.Errorf("hosting.json names needs %v: a python app speaks http and asks the edge for nothing", hosting.Needs)
	}
}

func TestCompileServesTheEntrypointAPythonAppNames(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":         "print('hi')\n",
		"api/__init__.py": "",
		"api/routes.py":   "app = None\n",
	})
	functionDir := filepath.Join(t.TempDir(), "index.func")
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		Entrypoint:  "api.routes:app",
		FunctionDir: functionDir,
		AppDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var config buildoutput.FunctionConfig
	readJSON(t, filepath.Join(functionDir, buildoutput.FunctionConfigFile), &config)
	if want := []string{containerimage.PythonWorkerEntry.Interpreter, pythonserver.File, "module", "api.routes:app"}; !slices.Equal(config.Command, want) {
		t.Errorf("command = %q, want %q", config.Command, want)
	}
}

func TestCompileRefusesAPythonEntrypointNamingNoFileInTheApp(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"main.py": "print('hi')\n"})
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		Entrypoint:  "api/main.py",
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "api/main.py") || !strings.Contains(err.Error(), `"web"`) {
		t.Fatalf("err = %v, want a refusal naming the app and the entrypoint — an entrypoint the build cannot find would ship an app that never starts", err)
	}
}

func TestVendoringAsksPipOnlyForWheelsTheDeclaredArchitectureCanImport(t *testing.T) {
	t.Parallel()

	for architecture, platform := range map[string]string{
		"x86_64": "manylinux2014_x86_64",
		"arm64":  "manylinux2014_aarch64",
	} {
		argv := strings.Join(pipArgs("/out", "/app/requirements.txt", platform), " ")
		for _, want := range []string{
			"--target /out",
			"-r /app/requirements.txt",
			"--only-binary=:all:",
			"--platform " + platform,
			"--python-version " + arch.PythonVersion,
			"--implementation cp",
			"--no-compile",
		} {
			if !strings.Contains(argv, want) {
				t.Errorf("pip is run as %q for %s, and it includes no %q: a wheel built for another machine or another python is installed silently and fails at import", argv, architecture, want)
			}
		}
	}
}

func TestCompileRefusesAPythonAppWithDependenciesAndNoInterpreterToVendorThemWith(t *testing.T) {
	source := pythonApp(t, map[string]string{
		"main.py":          "print('hi')\n",
		"requirements.txt": "six==1.17.0\n",
	})
	t.Setenv("PATH", "")

	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		Entrypoint:  "main.py",
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), containerimage.PythonWorkerEntry.Interpreter) || !strings.Contains(err.Error(), pythonRequirementsFile) {
		t.Fatalf("err = %v, want a refusal naming %s and %s — a package shipped without its dependencies fails at the app's first import instead", err, containerimage.PythonWorkerEntry.Interpreter, pythonRequirementsFile)
	}
}

func TestCompileVendorsWhatTheAppDeclaresIntoTheArtifact(t *testing.T) {
	t.Parallel()

	if !pipAvailable() {
		t.Skip("no python interpreter with pip on PATH to vendor with")
	}
	source := pythonApp(t, map[string]string{
		"main.py":          "import six\n",
		"requirements.txt": "six==1.17.0\n",
	})
	_, functionDir := vendored(t, source, "x86_64")

	if _, err := os.Stat(filepath.Join(functionDir, "six.py")); err != nil {
		t.Errorf("the artifact contains no six.py: a declared dependency is shipped in the package, since a function has no installer to reach for one: %v", err)
	}
}

func TestVendoringRunsTheInterpretersPipAndNeverAModuleOfTheApps(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath(containerimage.PythonWorkerEntry.Interpreter); err != nil {
		t.Skip("no python interpreter on PATH to vendor with")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	source := pythonApp(t, map[string]string{
		"main.py":          "print('hi')\n",
		"pip.py":           "open(" + strconv.Quote(marker) + ", 'w').write('the app ran')\n",
		"requirements.txt": "six==1.17.0\n",
	})
	c := Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	}

	_ = c.installDependencies(context.Background(), "manylinux2014_x86_64")

	if _, err := os.Stat(marker); err == nil {
		t.Error("vendoring ran the app's own pip.py, want the interpreter's pip whatever the app's directory holds")
	}
}

func TestCompileVendorsWhatAUvLockPinsIntoTheArtifact(t *testing.T) {
	t.Parallel()

	if !pipAvailable() {
		t.Skip("no python interpreter with pip on PATH to vendor with")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("no uv on PATH to export the lock with")
	}
	source := pythonApp(t, map[string]string{
		"main.py":        "import six\n",
		"pyproject.toml": "[project]\nname = \"web\"\nversion = \"0\"\nrequires-python = \">=3.12\"\ndependencies = [\"six==1.17.0\"]\n\n[tool.uv]\npackage = false\n",
	})
	lock := exec.Command("uv", "lock")
	lock.Dir = source
	if said, err := lock.CombinedOutput(); err != nil {
		t.Skipf("uv could not lock the fixture, so the index is out of reach: %v\n%s", err, said)
	}
	_, functionDir := vendored(t, source, "x86_64")

	if _, err := os.Stat(filepath.Join(functionDir, "six.py")); err != nil {
		t.Errorf("the artifact contains no six.py, which uv.lock pins: %v", err)
	}
}

func pipAvailable() bool {
	program, err := exec.LookPath(containerimage.PythonWorkerEntry.Interpreter)
	if err != nil {
		return false
	}
	return exec.Command(program, "-m", "pip", "--version").Run() == nil
}

func TestCompileRefusesAnArchitectureNoWheelIsBuiltFor(t *testing.T) {
	t.Parallel()

	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "riscv"},
		Source:      pythonApp(t, map[string]string{"main.py": "print('hi')\n"}),
		Entrypoint:  "main.py",
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "riscv") {
		t.Fatalf("err = %v, want a refusal naming riscv", err)
	}
}

func TestCompileRefusesAPythonAppContainingWhatCannotBeCopiedIntoTheArtifact(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"main.py": "print('hi')\n"})
	linked := filepath.Join(source, "settings.py")
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.py"), linked); err != nil {
		t.Fatal(err)
	}
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		Entrypoint:  "main.py",
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), linked) {
		t.Fatalf("err = %v, want a refusal naming %s — an artifact quietly missing a file the app imports fails only once it is running", err, linked)
	}
}

func TestCompileRefusesAPythonAppWhoseDeclaredDependenciesCannotBeRead(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":                   "print('hi')\n",
		"requirements.txt/is-a-dir": "six==1.17.0\n",
	})
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		Entrypoint:  "main.py",
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), pythonRequirementsFile) {
		t.Fatalf("err = %v, want a refusal naming %s — a package that silently ships without the dependencies it declares fails at the app's first import instead", err, pythonRequirementsFile)
	}
}

func TestVendoringReportsAnythingButAMissingRequirementsFile(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"main.py": "print('hi')\n"})
	looping := filepath.Join(source, pythonRequirementsFile)
	if err := os.Symlink(pythonRequirementsFile, looping); err != nil {
		t.Fatal(err)
	}
	c := Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:      source,
		FunctionDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:      t.TempDir(),
	}

	err := c.installDependencies(context.Background(), "manylinux2014_x86_64")
	if err == nil || !strings.Contains(err.Error(), pythonRequirementsFile) {
		t.Fatalf("err = %v, want the stat error naming %s — only a missing file says the app declares no dependencies", err, pythonRequirementsFile)
	}
}

func TestCompileLeavesTheBuildHostsOwnDirectoriesOutOfThePythonArtifact(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":                    "print('hi')\n",
		".venv/lib/six.py":           "vendored for the build host\n",
		".env":                       "SECRET=hunter2\n",
		".git/config":                "[core]\n",
		".DS_Store":                  "finder\n",
		"node_modules/left-pad/i.js": "module.exports = 1\n",
		"venv/pyvenv.cfg":            "home = /usr\n",
	})
	_, functionDir := vendored(t, source, "x86_64")

	for _, rel := range []string{".venv", ".env", ".git", ".DS_Store", "node_modules", "venv"} {
		if _, err := os.Stat(filepath.Join(functionDir, rel)); err == nil {
			t.Errorf("the artifact contains %s: what an interpreter or an installer leaves in the app directory contains the build host's paths and its secrets, neither of which the function runs on", rel)
		}
	}
}

func TestCompileCopiesTheDiscoveryRootsThePythonAppImportsIntoTheArtifact(t *testing.T) {
	t.Parallel()

	fixtureImport := "from infra import db\n"
	if imported := strings.Fields(fixtureImport)[1]; imported != discovery.DefaultRootDirName {
		t.Fatalf("the fixture imports %q, want the default discovery package %q", imported, discovery.DefaultRootDirName)
	}
	project := pythonApp(t, map[string]string{
		"server/main.py": fixtureImport,
		discovery.DefaultRootDirName + "/__init__.py":  "db = 1\n",
		discovery.DefaultRootDirName + "/nested/db.py": "handle = 2\n",
	})
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:            "web",
		Framework:      buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:         filepath.Join(project, "server"),
		Entrypoint:     "main.py",
		PythonPackages: []discovery.PythonPackage{{Name: discovery.DefaultRootDirName, Dir: filepath.Join(project, discovery.DefaultRootDirName)}},
		FunctionDir:    functionDir,
		AppDir:         appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	for _, rel := range []string{filepath.Join(discovery.DefaultRootDirName, "__init__.py"), filepath.Join(discovery.DefaultRootDirName, "nested", "db.py")} {
		if _, err := os.Stat(filepath.Join(functionDir, rel)); err != nil {
			t.Errorf("the artifact contains no %s: `from infra import db` resolves at runtime only if the folder that declared it travels with the app: %v", rel, err)
		}
	}
}

func TestCompileCopiesNoDiscoveryRootTheAppsOwnDirectoryAlreadyContains(t *testing.T) {
	t.Parallel()

	project := pythonApp(t, map[string]string{
		"server/main.py":             "print('hi')\n",
		"server/..infra/__init__.py": "db = 1\n",
	})
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:            "web",
		Framework:      buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:         filepath.Join(project, "server"),
		Entrypoint:     "main.py",
		PythonPackages: []discovery.PythonPackage{{Name: "..infra", Dir: filepath.Join(project, "server", "..infra")}},
		FunctionDir:    functionDir,
		AppDir:         appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(functionDir, "..infra")); err == nil {
		t.Error("the artifact contains ..infra: a root under the app directory is left to the app's own tree, whatever its name begins with")
	}
}

func TestCompileWritesTheWorkerEntryBesideAPythonAppThatImportsDiscoveryRoots(t *testing.T) {
	t.Parallel()

	project := pythonApp(t, map[string]string{
		"server/main.py":                              "print('hi')\n",
		"server/jobs/__init__.py":                     "",
		discovery.DefaultRootDirName + "/__init__.py": "db = 1\n",
	})
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:        "web",
		Framework:  buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:     filepath.Join(project, "server"),
		Entrypoint: "main.py",
		PythonPackages: []discovery.PythonPackage{
			{Name: discovery.DefaultRootDirName, Dir: filepath.Join(project, discovery.DefaultRootDirName)},
			{Name: "jobs", Dir: filepath.Join(project, "server", "jobs")},
		},
		FunctionDir: functionDir,
		AppDir:      appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var cfg buildoutput.FunctionConfig
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(functionDir, buildoutput.FunctionConfigFile))), &cfg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"python3", containerimage.PythonWorkerEntry.ArtifactFile}; !slices.Equal(cfg.Worker, want) {
		t.Errorf("%s worker = %v, want %v", buildoutput.FunctionConfigFile, cfg.Worker, want)
	}
	entry := readFile(t, filepath.Join(functionDir, containerimage.PythonWorkerEntry.ArtifactFile))
	for _, want := range []string{`import_module("` + discovery.DefaultRootDirName + `")`, `import_module("jobs")`, "sys.path.insert(0, os.getcwd())"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the worker entry lacks %s:\n%s", want, entry)
		}
	}
	if strings.Contains(entry, project) {
		t.Errorf("the worker entry names the build host's %s, which is nowhere the function runs", project)
	}
}

func TestCompileWritesNoWorkerEntryForAPythonAppWithNoDiscoveryRoot(t *testing.T) {
	t.Parallel()

	_, functionDir := vendored(t, pythonApp(t, map[string]string{"main.py": "print('hi')\n"}), "x86_64")

	if _, err := os.Stat(filepath.Join(functionDir, containerimage.PythonWorkerEntry.ArtifactFile)); err == nil {
		t.Errorf("the artifact contains %s, and nothing declares a resource a worker could serve", containerimage.PythonWorkerEntry.ArtifactFile)
	}
}

func TestCompileCopiesADiscoveryFolderThatIsTheAppsOwnDirectoryUnderTheNameItIsImportedBy(t *testing.T) {
	t.Parallel()

	project := pythonApp(t, map[string]string{
		"server/main.py":     "print('hi')\n",
		"server/__init__.py": "db = 1\n",
	})
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:            "web",
		Framework:      buildoutput.Framework{Name: "python", Arch: "x86_64"},
		Source:         filepath.Join(project, "server"),
		Entrypoint:     "main.py",
		PythonPackages: []discovery.PythonPackage{{Name: "server", Dir: filepath.Join(project, "server")}},
		FunctionDir:    functionDir,
		AppDir:         appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(functionDir, "server", "__init__.py")); err != nil {
		t.Errorf("the artifact holds no server/__init__.py, and its worker entry imports server from the artifact root: %v", err)
	}
}

const asgiApp = "async def app(scope, receive, send):\n    pass\n"

func refusedASGI(t *testing.T, entrypoint string, files map[string]string) error {
	t.Helper()
	if _, err := exec.LookPath(containerimage.PythonWorkerEntry.Interpreter); err != nil {
		t.Skip("no python interpreter on PATH to run the server program with")
	}
	files[pythonserver.File] = pythonserver.Source
	dir := pythonApp(t, files)
	resolved, err := pythonserver.ResolveEntrypoint(dir, entrypoint)
	if err != nil {
		t.Fatalf("ResolveEntrypoint(%q) = %v", entrypoint, err)
	}
	return Compilation{App: "web", Source: dir, FunctionDir: dir}.refuseUnservedASGI(context.Background(), resolved)
}

func TestAnArtifactServingAnASGIAppWithNoUvicornIsRefused(t *testing.T) {
	t.Parallel()

	err := refusedASGI(t, "main.py", map[string]string{
		"main.py":                             asgiApp,
		"starlette-0.47.2.dist-info/METADATA": "Name: starlette\n",
		"fastapi-0.116.1.dist-info/METADATA":  "Name: fastapi\n",
	})
	if err == nil || !strings.Contains(err.Error(), "starlette") || !strings.Contains(err.Error(), "uvicorn") {
		t.Fatalf("err = %v, want a refusal naming starlette and uvicorn — nothing in the artifact could serve the app", err)
	}
}

func TestAnArtifactServingAnASGIAppWithAServerOcelDoesNotRunIsRefused(t *testing.T) {
	t.Parallel()

	for _, server := range []string{"Hypercorn-0.17.3.dist-info", "granian-2.5.0.dist-info", "daphne-4.2.1.dist-info"} {
		err := refusedASGI(t, "main.py", map[string]string{
			"main.py":                         asgiApp,
			"Quart-0.20.0.dist-info/METADATA": "Name: Quart\n",
			server + "/METADATA":              "Name: server\n",
		})
		if err == nil || !strings.Contains(err.Error(), "uvicorn") {
			t.Errorf("with %s: err = %v, want a refusal naming uvicorn — the server program serves app with uvicorn alone, and the app would fail at boot", server, err)
		}
	}
}

func TestAnArtifactServingAnASGIAppWithUvicornIsAdmitted(t *testing.T) {
	t.Parallel()

	err := refusedASGI(t, "main.py", map[string]string{
		"main.py":                           asgiApp,
		"Quart-0.20.0.dist-info/METADATA":   "Name: Quart\n",
		"uvicorn-0.35.0.dist-info/METADATA": "Name: uvicorn\n",
	})
	if err != nil {
		t.Errorf("err = %v, want the artifact admitted — uvicorn serves the app", err)
	}
}

func TestAnASGIScriptThatStartsTheServerItInstallsIsAdmitted(t *testing.T) {
	t.Parallel()

	err := refusedASGI(t, "main.py", map[string]string{
		"main.py": `import asyncio

from hypercorn.asyncio import serve
from hypercorn.config import Config
from quart import Quart

web = Quart(__name__)

if __name__ == "__main__":
    asyncio.run(serve(web, Config()))
`,
		"Quart-0.20.0.dist-info/METADATA":     "Name: Quart\n",
		"Hypercorn-0.17.3.dist-info/METADATA": "Name: Hypercorn\n",
	})
	if err != nil {
		t.Errorf("err = %v, want the artifact admitted — the entrypoint defines no app, so the server program runs it as the script that serves itself", err)
	}
}

func TestAnArtifactHoldingNoASGIFrameworkIsAdmittedWithoutAServer(t *testing.T) {
	t.Parallel()

	err := refusedASGI(t, "main.py", map[string]string{
		"main.py":                        "app = None\n",
		"flask-3.1.1.dist-info/METADATA": "Name: Flask\n",
	})
	if err != nil {
		t.Errorf("err = %v, want a WSGI app admitted: the standard library serves it when gunicorn is absent", err)
	}
}
