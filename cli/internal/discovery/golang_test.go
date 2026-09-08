package discovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func goFixture(t *testing.T, module string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module "+module+"\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "infra", "infra.go"), "package infra\n")
	return root
}

func TestTheGoLauncherRunsAMainThatImportsTheInfraPackage(t *testing.T) {
	configDir := goFixture(t, "example.com/web")
	root := Root{Dir: filepath.Join(configDir, "infra"), Language: Go}

	cmd, err := launchers[Go].Command(context.Background(), configDir, root, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	moduleRoot := configDir
	if cmd.Dir != moduleRoot {
		t.Errorf("Dir = %q, want %q", cmd.Dir, moduleRoot)
	}
	if want := []string{"go", "run", "./.ocel/discovery"}; !slices.Equal(cmd.Args[1:], want[1:]) || filepath.Base(cmd.Args[0]) != "go" {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}
	for _, want := range []string{"OCEL_PHASE=discovery", "OCEL_DEV_SERVER=http://127.0.0.1:1234"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("Env lacks %q", want)
		}
	}

	generated, err := os.ReadFile(filepath.Join(moduleRoot, ".ocel", "discovery", "main.go"))
	if err != nil {
		t.Fatalf("read the generated main: %v", err)
	}
	if !strings.Contains(string(generated), `_ "example.com/web/infra"`) {
		t.Errorf("generated main = %q, want it to import the infra package", generated)
	}
}

func TestTheGoLauncherRefusesARootWithNoModuleAboveIt(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "infra", "infra.go"), "package infra\n")

	_, err := launchers[Go].Command(context.Background(), configDir, Root{Dir: filepath.Join(configDir, "infra"), Language: Go}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded with no go.mod above the root, want error")
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("error = %q, want it to name the missing go.mod", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(configDir, "infra")) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func repoFixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", name))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}
	return dir
}

func TestRunDeclaresWhatTheGoFixtureDeclares(t *testing.T) {
	configDir := repoFixture(t, filepath.Join("sdk", "go"))

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Go || roots[0].Dir != filepath.Join(configDir, "infra") {
		t.Fatalf("roots = %+v, want the go infra folder of the project", roots)
	}

	collected, url := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, url, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	declares := collected.declared()
	if len(declares) != 1 {
		t.Fatalf("declares = %v, want exactly one", declares)
	}
	resource := declares[0].GetResource()
	if resource.GetName() != "main" || resource.GetType() != linksv1.LinkType_LINK_TYPE_POSTGRES {
		t.Errorf("resource = %v, want the postgres named main", resource)
	}
	source := declares[0].GetSource()
	want := filepath.ToSlash(filepath.Join("infra", "infra.go")) + ":5"
	if !strings.HasSuffix(filepath.ToSlash(source), want) {
		t.Errorf("source = %q, want it to end with %q", source, want)
	}
}

func TestRunReportsWhatAGoRootThatDoesNotCompileSaid(t *testing.T) {
	configDir := goFixture(t, "example.com/web")
	write(t, filepath.Join(configDir, "infra", "infra.go"), "package infra\n\nvar DB = undeclared()\n")

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), configDir, prepared, "http://127.0.0.1:1", &stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded on a root that does not compile, want error")
	}
	if !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("error = %q, want it to carry the compile error", err)
	}
}
