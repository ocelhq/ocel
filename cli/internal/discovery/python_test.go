package discovery

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func pythonFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "declarations", "__init__.py"), "")
	return root
}

func TestPythonDiscoveryDerivesTheImportedPackageFromTheRoot(t *testing.T) {
	configDir := pythonFixture(t)
	root := Root{Dir: filepath.Join(configDir, "declarations"), Language: language.Python}

	commands, err := pythonCommands(context.Background(), configDir, []Root{root}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cmd := commands[0]

	if cmd.Dir != configDir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, configDir)
	}
	script := cmd.Args[len(cmd.Args)-1]
	if !strings.HasPrefix(script, "./"+buildoutput.StateDir+"/discovery/") || !strings.HasSuffix(script, "/discovery.py") {
		t.Errorf("Args = %q, want them to run a discovery.py under %s/discovery", cmd.Args, buildoutput.StateDir)
	}
	for _, want := range []string{processenv.PhaseEnvVar + "=discovery", processenv.DevServerEnvVar + "=http://127.0.0.1:1234", processenv.DevServerTokenEnvVar + "=opensesame", "PYTHONDONTWRITEBYTECODE=1"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("Env lacks %q", want)
		}
	}

	generated, err := os.ReadFile(filepath.Join(configDir, filepath.FromSlash(script)))
	if err != nil {
		t.Fatalf("read the generated script: %v", err)
	}
	if !strings.Contains(string(generated), `import_module("declarations")`) {
		t.Errorf("generated script = %q, want it to import the package named by the root", generated)
	}
}

func TestPythonDiscoveryRunsFromTheNearestProjectFileAboveTheRoot(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "pyproject.toml"), "")
	write(t, filepath.Join(configDir, "server", "requirements.txt"), "")
	write(t, filepath.Join(configDir, "server", "declarations", "__init__.py"), "")

	commands, err := pythonCommands(context.Background(), configDir, []Root{{Dir: filepath.Join(configDir, "server", "declarations"), Language: language.Python}}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cmd := commands[0]
	if want := filepath.Join(configDir, "server"); cmd.Dir != want {
		t.Errorf("Dir = %q, want %q", cmd.Dir, want)
	}
}

func TestPythonDiscoveryRefusesARootTooDeepToNameAPackage(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "requirements.txt"), "")
	root := filepath.Join(configDir, "server", "declarations")
	write(t, filepath.Join(root, "__init__.py"), "")

	_, err := pythonCommands(context.Background(), configDir, []Root{{Dir: root, Language: language.Python}}, testServer)
	if err == nil {
		t.Fatal("Command succeeded on a root nested under the project file, want error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestRunDeclaresWhatThePythonFixtureDeclares(t *testing.T) {
	configDir := repoFixture(t, "python")
	app, err := os.ReadFile(filepath.Join(configDir, "server", "main.py"))
	if err != nil {
		t.Fatalf("read the fixture app: %v", err)
	}
	if !strings.Contains(string(app), "from "+DefaultRootDirName+" import") {
		t.Fatalf("the fixture app does not import the default discovery package")
	}

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != language.Python || roots[0].Dir != filepath.Join(configDir, DefaultRootDirName) {
		t.Fatalf("roots = %+v, want the python infra folder of the project", roots)
	}

	collected, server := declareCollector(t)

	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, server, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	declares := collected.declared()
	if len(declares) != 2 {
		t.Fatalf("declares = %v, want the postgres and the bucket the fixture declares", declares)
	}
	declared := map[resourcesv1.ResourceType]*resourcesv1.DeclareRequest{}
	for _, request := range declares {
		declared[request.GetResource().GetType()] = request
	}
	for _, want := range []struct {
		kind resourcesv1.ResourceType
		name string
		line string
	}{
		{resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "main", ":3"},
		{resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, "uploads", ":10"},
	} {
		request, found := declared[want.kind]
		if !found {
			t.Fatalf("declares = %v, want one of %v", declares, want.kind)
		}
		if request.GetResource().GetName() != want.name {
			t.Errorf("resource = %v, want %q", request.GetResource(), want.name)
		}
		source := filepath.ToSlash(filepath.Join(DefaultRootDirName, "__init__.py")) + want.line
		if !strings.HasSuffix(filepath.ToSlash(request.GetSource()), source) {
			t.Errorf("source = %q, want it to end with %q", request.GetSource(), source)
		}
	}

	variables := collected.declaredVariables()
	if len(variables) != 1 || variables[0].GetKey() != "GREETING" || variables[0].GetRequired() {
		t.Fatalf("variables = %v, want the one defaulted GREETING", variables)
	}
	if want := filepath.ToSlash(filepath.Join(DefaultRootDirName, "__init__.py")) + ":6"; !strings.HasSuffix(filepath.ToSlash(variables[0].GetSource()), want) {
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
		t.Skipf("the python sdk and its dependencies are not installed: run `uv sync --all-extras` in python/ (%v)", err)
	}
	return dir
}

func TestPythonInterpreterPrefersTheVirtualenvBesideTheRunRoot(t *testing.T) {
	runRoot := t.TempDir()
	if got := PythonInterpreter(runRoot); got != "python3" {
		t.Errorf("PythonInterpreter = %q, want the interpreter on PATH where the run root has no virtualenv", got)
	}

	venv := filepath.Join(runRoot, ".venv", "bin", "python")
	write(t, venv, "")
	if got := PythonInterpreter(runRoot); got != venv {
		t.Errorf("PythonInterpreter = %q, want %q", got, venv)
	}
}

func twoPythonApps(t *testing.T) (string, []Root) {
	t.Helper()
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "a", "pyproject.toml"), "")
	write(t, filepath.Join(configDir, "a", "jobs", "__init__.py"), "")
	write(t, filepath.Join(configDir, "b", "pyproject.toml"), "")
	write(t, filepath.Join(configDir, "b", "tasks", "__init__.py"), "")
	write(t, filepath.Join(configDir, DefaultRootDirName, "__init__.py"), "")
	return configDir, []Root{
		{Dir: filepath.Join(configDir, "a", "jobs"), Language: language.Python},
		{Dir: filepath.Join(configDir, "b", "tasks"), Language: language.Python},
		{Dir: filepath.Join(configDir, DefaultRootDirName), Language: language.Python},
		{Dir: filepath.Join(configDir, "web"), Language: language.JS},
	}
}

func TestAPythonAppImportsTheFoldersOfItsOwnProjectAndThoseOfNoProjectButNeverAnotherAppsFolders(t *testing.T) {
	configDir, roots := twoPythonApps(t)

	packages, err := ListPythonPackages(configDir, roots, filepath.Join(configDir, "a"))
	if err != nil {
		t.Fatalf("ListPythonPackages: %v", err)
	}
	want := []PythonPackage{
		{Name: "jobs", Dir: filepath.Join(configDir, "a", "jobs")},
		{Name: DefaultRootDirName, Dir: filepath.Join(configDir, DefaultRootDirName)},
	}
	if !slices.Equal(packages, want) {
		t.Errorf("packages = %+v, want %+v: b/tasks belongs to b's pyproject and is nowhere app a runs", packages, want)
	}
}

func TestListingPythonPackagesRefusesAFolderTooDeepToNameAPackage(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "server", "pyproject.toml"), "")
	nested := filepath.Join(configDir, "server", "lib", "jobs")
	write(t, filepath.Join(nested, "__init__.py"), "")

	_, err := ListPythonPackages(configDir, []Root{{Dir: nested, Language: language.Python}}, filepath.Join(configDir, "server"))
	if err == nil || !strings.Contains(err.Error(), nested) {
		t.Fatalf("ListPythonPackages = %v, want a refusal naming %s", err, nested)
	}
}

func TestListingPythonPackagesRefusesTwoFoldersImportedByOneName(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "server", "pyproject.toml"), "")
	own := filepath.Join(configDir, "server", DefaultRootDirName)
	shared := filepath.Join(configDir, DefaultRootDirName)
	write(t, filepath.Join(own, "__init__.py"), "")
	write(t, filepath.Join(shared, "__init__.py"), "")

	_, err := ListPythonPackages(configDir, []Root{{Dir: own, Language: language.Python}, {Dir: shared, Language: language.Python}}, filepath.Join(configDir, "server"))
	if err == nil || !strings.Contains(err.Error(), own) || !strings.Contains(err.Error(), shared) {
		t.Fatalf("ListPythonPackages = %v, want a refusal naming both folders imported as %q", err, DefaultRootDirName)
	}
}

func TestListingPythonPackagesRefusesAFolderThatIsItsProjectsOwnDirectory(t *testing.T) {
	configDir := t.TempDir()
	server := filepath.Join(configDir, "server")
	write(t, filepath.Join(server, "pyproject.toml"), "")
	write(t, filepath.Join(server, "main.py"), "")

	_, err := ListPythonPackages(configDir, []Root{{Dir: server, Language: language.Python}}, server)
	if err == nil || !strings.Contains(err.Error(), server) {
		t.Fatalf("ListPythonPackages = %v, want a refusal naming %s, which no import names", err, server)
	}
}

func TestRunImportsEveryPythonRootThatSharesARunRootInOneProgram(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "pyproject.toml"), "")
	for _, pkg := range []string{"jobs", "tasks"} {
		write(t, filepath.Join(configDir, pkg, "__init__.py"), "print(\"declared from "+pkg+"\")\n")
	}
	roots, err := Roots(configDir, []string{"jobs", "tasks"})
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	commands, err := commandsFor(context.Background(), configDir, prepared, testServer)
	if err != nil {
		t.Fatalf("commandsFor: %v", err)
	}
	if len(commands) != 1 {
		t.Errorf("commands = %d, want one program for the one run root", len(commands))
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	for _, want := range []string{"declared from jobs", "declared from tasks"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q: every root under the run root is imported", stdout.String(), want)
		}
	}
}
