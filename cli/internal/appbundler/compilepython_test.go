package appbundler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func vendored(t *testing.T, source, arch string) (string, string) {
	t.Helper()
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	funcDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: arch},
		Source:  source,
		FuncDir: funcDir,
		AppDir:  appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return appDir, funcDir
}

func TestCompileRefusesAPythonAppDirectoryHoldingNoEntrypoint(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"server/main.py": "print('hi')\n"})
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "x86_64"},
		Source:  source,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), pythonEntryFile) {
		t.Fatalf("err = %v, want a refusal naming %s and %s — nothing else in the directory says which module serves the app", err, source, pythonEntryFile)
	}
}

func TestCompileCarriesThePythonAppsOwnSourceIntoTheArtifact(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":              "print('hi')\n",
		"probes.py":            "answer = 42\n",
		"static/ocel.svg":      "<svg/>\n",
		"__pycache__/main.pyc": "stale bytecode\n",
	})
	_, funcDir := vendored(t, source, "x86_64")

	for _, rel := range []string{"main.py", "probes.py", filepath.Join("static", "ocel.svg")} {
		if _, err := os.Stat(filepath.Join(funcDir, rel)); err != nil {
			t.Errorf("the artifact holds no %s: a python app is served from the files it was written as, not from a bundle: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(funcDir, "__pycache__")); err == nil {
		t.Error("the artifact carries __pycache__, whose bytecode was compiled by whatever python built the app rather than the one that runs it")
	}
}

func TestCompileDeclaresTheCommandAPythonArtifactIsServedBy(t *testing.T) {
	t.Parallel()

	appDir, funcDir := vendored(t, pythonApp(t, map[string]string{"main.py": "print('hi')\n"}), "arm64")

	var config functionConfig
	readJSON(t, filepath.Join(funcDir, configFileName), &config)
	if config.Handler != pythonEntryFile {
		t.Errorf("handler = %q, want %q — Lambda refuses a package whose handler names no file in it", config.Handler, pythonEntryFile)
	}
	if len(config.Command) != 2 || config.Command[0] != pythonProgram || config.Command[1] != pythonEntryFile {
		t.Errorf("command = %q, want the interpreter and the module it runs, which whatever hosts the artifact execs", config.Command)
	}
	if config.Runtime != (Runtime{Name: "python", Arch: "arm64"}) {
		t.Errorf("runtime = %+v, want the python runtime at the architecture it was vendored for", config.Runtime)
	}
	if config.App != "web" {
		t.Errorf("app = %q, want %q", config.App, "web")
	}

	var descriptor edge.ServeDescriptor
	readJSON(t, filepath.Join(appDir, edge.ServeDescriptorFile), &descriptor)
	if descriptor.Runtime != "python" {
		t.Errorf("the serve descriptor names runtime %q, want %q", descriptor.Runtime, "python")
	}
	if descriptor.BuildID == "" {
		t.Error("the serve descriptor names no build id, and a release is identified by one")
	}
	if len(descriptor.Needs) != 0 {
		t.Errorf("the serve descriptor names needs %v: a python app speaks http and asks the edge for nothing", descriptor.Needs)
	}
}

func TestCompileRefusesAPythonAppThatNamesAnEntrypointOfItsOwn(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":     "print('hi')\n",
		"api/main.py": "print('hi')\n",
	})
	err := Compile(context.Background(), Compilation{
		App:        "web",
		Runtime:    Runtime{Name: "python", Arch: "x86_64"},
		Source:     source,
		Entrypoint: "api",
		FuncDir:    filepath.Join(t.TempDir(), "index.func"),
		AppDir:     t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), pythonEntryFile) {
		t.Fatalf("err = %v, want a refusal naming %s — an entrypoint the build ignores would ship a different app than the one it names", err, pythonEntryFile)
	}
}

func TestCompileRefusesAPythonAppsEntrypointBeforeLookingForTheDirectoryItNames(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"main.py": "print('hi')\n"})
	err := Compile(context.Background(), Compilation{
		App:        "web",
		Runtime:    Runtime{Name: "python", Arch: "x86_64"},
		Source:     source,
		Entrypoint: "api",
		FuncDir:    filepath.Join(t.TempDir(), "index.func"),
		AppDir:     t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), pythonEntryFile) {
		t.Fatalf("err = %v, want the refusal naming %s — an entrypoint the python build ignores is wrong whether or not the directory it names exists, and a stat error says nothing about that", err, pythonEntryFile)
	}
}

func TestVendoringAsksPipOnlyForWheelsTheDeclaredArchitectureCanImport(t *testing.T) {
	t.Parallel()

	for arch, platform := range map[string]string{
		"x86_64": "manylinux2014_x86_64",
		"arm64":  "manylinux2014_aarch64",
	} {
		argv := strings.Join(pipArgs("/out", "/app/requirements.txt", platform), " ")
		for _, want := range []string{
			"--target /out",
			"-r /app/requirements.txt",
			"--only-binary=:all:",
			"--platform " + platform,
			"--python-version " + providerkit.PythonVersion,
			"--implementation cp",
			"--no-compile",
		} {
			if !strings.Contains(argv, want) {
				t.Errorf("pip is run as %q for %s, and it carries no %q: a wheel built for another machine or another python is installed silently and fails at import", argv, arch, want)
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
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "x86_64"},
		Source:  source,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), pythonProgram) || !strings.Contains(err.Error(), pythonRequirementsFile) {
		t.Fatalf("err = %v, want a refusal naming %s and %s — a package shipped without its dependencies fails at the app's first import instead", err, pythonProgram, pythonRequirementsFile)
	}
}

func TestCompileVendorsWhatTheAppDeclaresIntoTheArtifact(t *testing.T) {
	t.Parallel()

	if !pipAvailable() {
		t.Skip("no python interpreter carrying pip on PATH to vendor with")
	}
	source := pythonApp(t, map[string]string{
		"main.py":          "import six\n",
		"requirements.txt": "six==1.17.0\n",
	})
	_, funcDir := vendored(t, source, "x86_64")

	if _, err := os.Stat(filepath.Join(funcDir, "six.py")); err != nil {
		t.Errorf("the artifact holds no six.py: a declared dependency is carried in the package, since a function has no installer to reach for one: %v", err)
	}
}

func pipAvailable() bool {
	program, err := exec.LookPath(pythonProgram)
	if err != nil {
		return false
	}
	return exec.Command(program, "-m", "pip", "--version").Run() == nil
}

func TestCompileRefusesAnArchitectureNoWheelIsBuiltFor(t *testing.T) {
	t.Parallel()

	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "riscv"},
		Source:  pythonApp(t, map[string]string{"main.py": "print('hi')\n"}),
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "riscv") {
		t.Fatalf("err = %v, want a refusal naming riscv", err)
	}
}

func TestCompileRefusesAPythonAppCarryingWhatCannotBeCopiedIntoTheArtifact(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{"main.py": "print('hi')\n"})
	linked := filepath.Join(source, "settings.py")
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.py"), linked); err != nil {
		t.Fatal(err)
	}
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "x86_64"},
		Source:  source,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), linked) {
		t.Fatalf("err = %v, want a refusal naming %s — an artifact quietly missing a file the app imports fails only once it is running", err, linked)
	}
}

func TestCompileRefusesAPythonAppWhoseDeclaredDependenciesCannotBeRead(t *testing.T) {
	t.Parallel()

	source := pythonApp(t, map[string]string{
		"main.py":                      "print('hi')\n",
		"requirements.txt/held-as-dir": "six==1.17.0\n",
	})
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "x86_64"},
		Source:  source,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
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
		App:     "web",
		Runtime: Runtime{Name: "python", Arch: "x86_64"},
		Source:  source,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	}

	err := c.installRequirements(context.Background(), "manylinux2014_x86_64")
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
	_, funcDir := vendored(t, source, "x86_64")

	for _, rel := range []string{".venv", ".env", ".git", ".DS_Store", "node_modules", "venv"} {
		if _, err := os.Stat(filepath.Join(funcDir, rel)); err == nil {
			t.Errorf("the artifact carries %s: what an interpreter or an installer leaves in the app directory holds the build host's paths and its secrets, neither of which the function runs on", rel)
		}
	}
}
