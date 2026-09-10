package discovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func pythonFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "declarations", "__init__.py"), "")
	return root
}

func TestThePythonLauncherDerivesTheImportedPackageFromTheRoot(t *testing.T) {
	configDir := pythonFixture(t)
	root := Root{Dir: filepath.Join(configDir, "declarations"), Language: Python}

	cmd, err := launchers[Python].Command(context.Background(), configDir, root, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if cmd.Dir != configDir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, configDir)
	}
	if want := "./" + constants.ProjectStateDirName + "/discovery.py"; !slices.Contains(cmd.Args, want) {
		t.Errorf("Args = %q, want them to run %q", cmd.Args, want)
	}
	for _, want := range []string{constants.PhaseEnvName + "=discovery", constants.DevServerEnvName + "=http://127.0.0.1:1234", "PYTHONDONTWRITEBYTECODE=1"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("Env lacks %q", want)
		}
	}

	generated, err := os.ReadFile(filepath.Join(configDir, constants.ProjectStateDirName, "discovery.py"))
	if err != nil {
		t.Fatalf("read the generated script: %v", err)
	}
	if !strings.Contains(string(generated), `import_module("declarations")`) {
		t.Errorf("generated script = %q, want it to import the package named by the root", generated)
	}
}

func TestThePythonLauncherRunsFromTheNearestProjectFileAboveTheRoot(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "pyproject.toml"), "")
	write(t, filepath.Join(configDir, "server", "requirements.txt"), "")
	write(t, filepath.Join(configDir, "server", "declarations", "__init__.py"), "")

	cmd, err := launchers[Python].Command(context.Background(), configDir, Root{Dir: filepath.Join(configDir, "server", "declarations"), Language: Python}, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := filepath.Join(configDir, "server"); cmd.Dir != want {
		t.Errorf("Dir = %q, want %q", cmd.Dir, want)
	}
}

func TestThePythonLauncherRefusesARootTooDeepToNameAPackage(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "requirements.txt"), "")
	root := filepath.Join(configDir, "server", "declarations")
	write(t, filepath.Join(root, "__init__.py"), "")

	_, err := launchers[Python].Command(context.Background(), configDir, Root{Dir: root, Language: Python}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a root nested under the project file, want error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestRunDeclaresWhatThePythonFixtureDeclares(t *testing.T) {
	configDir := repoFixture(t, filepath.Join("sdk", "python"))
	app, err := os.ReadFile(filepath.Join(configDir, "server", "main.py"))
	if err != nil {
		t.Fatalf("read the fixture app: %v", err)
	}
	if !strings.Contains(string(app), "from "+constants.DefaultDiscoveryDirName+" import") {
		t.Fatalf("the fixture app does not import the default discovery package")
	}

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Python || roots[0].Dir != filepath.Join(configDir, constants.DefaultDiscoveryDirName) {
		t.Fatalf("roots = %+v, want the python infra folder of the project", roots)
	}

	collected, url := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, url, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	declares := collected.declared()
	if len(declares) != 1 {
		t.Fatalf("declares = %v, want exactly one", declares)
	}
	resource := declares[0].GetResource()
	if resource.GetName() != "main" || resource.GetType() != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Errorf("resource = %v, want the postgres named main", resource)
	}
	want := filepath.ToSlash(filepath.Join(constants.DefaultDiscoveryDirName, "__init__.py")) + ":3"
	if source := filepath.ToSlash(declares[0].GetSource()); !strings.HasSuffix(source, want) {
		t.Errorf("source = %q, want it to end with %q", source, want)
	}

	variables := collected.declaredVariables()
	if len(variables) != 1 || variables[0].GetKey() != "GREETING" || variables[0].GetRequired() {
		t.Fatalf("variables = %v, want the one defaulted GREETING", variables)
	}
	if want := filepath.ToSlash(filepath.Join(constants.DefaultDiscoveryDirName, "__init__.py")) + ":6"; !strings.HasSuffix(filepath.ToSlash(variables[0].GetSource()), want) {
		t.Errorf("variable source = %q, want it to end with %q", variables[0].GetSource(), want)
	}
}

func pythonSDKEnvironment(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "python", ".venv", "bin"))
	if err != nil {
		t.Fatalf("locate the python workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "python3")); err != nil {
		t.Fatalf("the python sdk and its dependencies are not installed: run `uv sync --all-extras` in python/ (%v)", err)
	}
	return dir
}

func TestPythonInterpreterPrefersTheVirtualenvBesideTheRunRoot(t *testing.T) {
	runRoot := t.TempDir()
	if got := PythonInterpreter(runRoot); got != "python3" {
		t.Errorf("PythonInterpreter = %q, want the interpreter on PATH where the run root holds no virtualenv", got)
	}

	venv := filepath.Join(runRoot, ".venv", "bin", "python")
	write(t, venv, "")
	if got := PythonInterpreter(runRoot); got != venv {
		t.Errorf("PythonInterpreter = %q, want %q", got, venv)
	}
}
