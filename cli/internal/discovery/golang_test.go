package discovery

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func goFixture(t *testing.T, module string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "server", "go.mod"), "module "+module+"\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "server", "infra", "infra.go"), "package infra\n")
	return root
}

func TestTheGoLauncherRunsAMainThatImportsTheInfraPackage(t *testing.T) {
	configDir := goFixture(t, "example.com/web")
	roots, err := Roots(configDir, nil, []string{"./server"})
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Go {
		t.Fatalf("roots = %+v, want one go root", roots)
	}

	cmd, err := launchers[Go].Command(context.Background(), configDir, roots[0], "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	moduleRoot := filepath.Join(configDir, "server")
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
}
