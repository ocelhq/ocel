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

func goFixture(t *testing.T, module string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module "+module+"\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "declarations", "declarations.go"), "package declarations\n")
	return root
}

func TestGoDiscoveryDerivesTheImportedPackageFromTheRoot(t *testing.T) {
	configDir := goFixture(t, "example.com/web")
	root := Root{Dir: filepath.Join(configDir, "declarations"), Language: language.Go}

	commands, err := goCommands(context.Background(), configDir, []Root{root}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cmd := commands[0]

	moduleRoot := configDir
	if cmd.Dir != moduleRoot {
		t.Errorf("Dir = %q, want %q", cmd.Dir, moduleRoot)
	}
	pkg := cmd.Args[len(cmd.Args)-1]
	if want := []string{"go", "run", "-trimpath=false"}; !slices.Equal(cmd.Args[1:3], want[1:]) || filepath.Base(cmd.Args[0]) != "go" || !strings.HasPrefix(pkg, "./"+buildoutput.StateDir+"/discovery/") {
		t.Errorf("Args = %q, want %q and a package under %s/discovery", cmd.Args, want, buildoutput.StateDir)
	}
	for _, want := range []string{processenv.PhaseEnvVar + "=discovery", processenv.DevServerEnvVar + "=http://127.0.0.1:1234", processenv.DevServerTokenEnvVar + "=opensesame"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("Env lacks %q", want)
		}
	}

	generated, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(pkg), "main.go"))
	if err != nil {
		t.Fatalf("read the generated main: %v", err)
	}
	if !strings.Contains(string(generated), `_ "example.com/web/declarations"`) {
		t.Errorf("generated main = %q, want it to import the package named by the root", generated)
	}
}

func TestGoDiscoveryRefusesARootWithNoModuleAboveIt(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "declarations", "declarations.go"), "package declarations\n")

	_, err := goCommands(context.Background(), configDir, []Root{{Dir: filepath.Join(configDir, "declarations"), Language: language.Go}}, testServer)
	if err == nil {
		t.Fatal("Command succeeded with no go.mod above the root, want error")
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("error = %q, want it to name the missing go.mod", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(configDir, "declarations")) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestGoDiscoveryLooksForNoModuleAboveTheProjectRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, filepath.Join(outer, "go.mod"), "module example.com/monorepo\n\ngo 1.24\n")
	configDir := filepath.Join(outer, "deploy")
	write(t, filepath.Join(configDir, "declarations", "declarations.go"), "package declarations\n")

	_, err := goCommands(context.Background(), configDir, []Root{{Dir: filepath.Join(configDir, "declarations"), Language: language.Go}}, testServer)
	if err == nil || !strings.Contains(err.Error(), configDir) {
		t.Fatalf("goCommands() = %v, want a refusal naming the project root %s — the go.mod above it is not the project's, as the detector and the build also hold", err, configDir)
	}
}

func repoFixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}
	return dir
}

func TestRunDeclaresWhatTheGoFixtureDeclares(t *testing.T) {
	configDir := repoFixture(t, "go")
	app, err := os.ReadFile(filepath.Join(configDir, "server", "main.go"))
	if err != nil {
		t.Fatalf("read the fixture app: %v", err)
	}
	if !strings.Contains(string(app), "example.com/declares/"+DefaultRootDirName) {
		t.Fatalf("the fixture app does not import the default discovery package")
	}

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != language.Go || roots[0].Dir != filepath.Join(configDir, DefaultRootDirName) {
		t.Fatalf("roots = %+v, want the go infra folder of the project", roots)
	}

	collected, server := declareCollector(t)

	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

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
		{resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "main", ":9"},
		{resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, "uploads", ":15"},
	} {
		request, found := declared[want.kind]
		if !found {
			t.Fatalf("declares = %v, want one of %v", declares, want.kind)
		}
		if request.GetResource().GetName() != want.name {
			t.Errorf("resource = %v, want %q", request.GetResource(), want.name)
		}
		source := filepath.ToSlash(filepath.Join(DefaultRootDirName, "infra.go")) + want.line
		if !strings.HasSuffix(filepath.ToSlash(request.GetSource()), source) {
			t.Errorf("source = %q, want it to end with %q", request.GetSource(), source)
		}
	}

	variables := collected.declaredVariables()
	if len(variables) != 1 || variables[0].GetKey() != "GREETING" || variables[0].GetRequired() {
		t.Fatalf("variables = %v, want the one defaulted GREETING", variables)
	}
	if want := filepath.ToSlash(filepath.Join(DefaultRootDirName, "infra.go")) + ":13"; !strings.HasSuffix(filepath.ToSlash(variables[0].GetSource()), want) {
		t.Errorf("variable source = %q, want it to end with %q", variables[0].GetSource(), want)
	}
}

func TestRunNamesTheDeclaringFileByItsPathWhenGOFLAGSTrimsPaths(t *testing.T) {
	t.Setenv("GOFLAGS", "-trimpath")
	configDir := repoFixture(t, "go")
	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	collected, server := declareCollector(t)
	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, server, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	infra := filepath.Join(configDir, DefaultRootDirName, "infra.go")
	for _, request := range collected.declared() {
		if file, _, _ := strings.Cut(request.GetSource(), ".go:"); file+".go" != infra {
			t.Errorf("source = %q, want the line in %s", request.GetSource(), infra)
		}
	}
}

func TestRunReportsWhatAGoRootThatDoesNotCompileSaid(t *testing.T) {
	configDir := goFixture(t, "example.com/web")
	write(t, filepath.Join(configDir, "declarations", "declarations.go"), "package declarations\n\nvar DB = undeclared()\n")

	roots, err := Roots(configDir, []string{"declarations"})
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), configDir, prepared, Server{URL: "http://127.0.0.1:1", Token: testToken}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded on a root that does not compile, want error")
	}
	if !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("error = %q, want it to include the compile error", err)
	}
}

func TestRunImportsEveryGoRootThatSharesAModuleInOneProgram(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "go.mod"), "module example.com/monorepo\n\ngo 1.27.0\n")
	for _, service := range []string{"a", "b"} {
		write(t, filepath.Join(configDir, "services", service, DefaultRootDirName, "declarations.go"), "package declarations\n\nimport \"fmt\"\n\nfunc init() { fmt.Println(\"declared from "+service+"\") }\n")
	}
	roots, err := Roots(configDir, []string{"services/a/" + DefaultRootDirName, "services/b/" + DefaultRootDirName})
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
		t.Errorf("commands = %d, want one program for the one module", len(commands))
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	for _, want := range []string{"declared from a", "declared from b"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q: every root in the module is imported", stdout.String(), want)
		}
	}
}
