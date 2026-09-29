package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestAContainerAppIsBuiltFromTheWorkspaceItIsAMemberOf(t *testing.T) {
	cfg := containerProject(t, "")
	if err := os.WriteFile(filepath.Join(cfg.Dir, "pnpm-workspace.yaml"), []byte("packages:\n  - services/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	built, err := Describe(cfg, cfg.Apps[0])
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}

	if built.Workspace.Root != cfg.Dir {
		t.Errorf("the image is built from %q, want the workspace root %q, where the lockfile the install reads lives", built.Workspace.Root, cfg.Dir)
	}
	if built.Workspace.Path != "services/web" {
		t.Errorf("the app sits at %q inside the context, want %q, which is what scopes the install and the build to it", built.Workspace.Path, "services/web")
	}
}

func TestABuildContextTheAppDoesNotSitUnderIsRefusedByName(t *testing.T) {
	cfg := containerProject(t, "")
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Apps[0].Build = &project.Build{Context: "elsewhere"}

	_, err := Describe(cfg, cfg.Apps[0])
	if err == nil {
		t.Fatal("Describe() accepted a build.context the app is not inside, so the image would be built without the app in it")
	}
	if !strings.Contains(err.Error(), "build.context") || !strings.Contains(err.Error(), "web") {
		t.Errorf("Describe() = %v, want the app and the key it got wrong named", err)
	}
}

func containerProject(t *testing.T, dockerfile string) *project.Project {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, "services", "web")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if dockerfile != "" {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(dockerfile)), []byte("FROM scratch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "services/web", Compute: "container"}},
	}
}
