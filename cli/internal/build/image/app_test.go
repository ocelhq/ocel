package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
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
	cfg.Apps[0].Container = &project.Container{Build: &project.Build{Context: "elsewhere"}}

	_, err := Describe(cfg, cfg.Apps[0])
	if err == nil {
		t.Fatal("Describe() accepted a image.context the app is not inside, so the image would be built without the app in it")
	}
	if !strings.Contains(err.Error(), "image.context") || !strings.Contains(err.Error(), "web") {
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
		Apps: []project.App{{Name: "web", Path: "services/web", Compute: "container", Language: language.JS}},
	}
}

func loadedUnderAGoModule(t *testing.T, files map[string]string) *project.Project {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/shop\n\ngo 1.25.0\n"
	files["main.go"] = "package main\n\nfunc main() {}\n"
	files[project.DefaultFileName] = `{"slug":"shop","apps":[{"name":"web","path":"web","compute":"container"}]}`
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := project.Load(t.Context(), root, "")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return cfg
}

func TestAnAppBelowARootGoModuleIsBuiltAsGoOnlyWhenItIsAGoApp(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"a node app started by its own script": {"web/package.json": `{"scripts":{"start":"node server.js"}}`, "web/server.js": "\n"},
		"an app built from its Dockerfile":     {"web/Dockerfile": "FROM scratch\n"},
		"a next app beside a go main package":  {"web/package.json": `{"dependencies":{"next":"15"}}`, "web/main.go": "package main\n\nfunc main() {}\n", "web/go.mod": "module example.com/web\n"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loadedUnderAGoModule(t, files)

			built, err := Describe(cfg, cfg.Apps[0])
			if err != nil {
				t.Fatalf("Describe() = %v", err)
			}
			if built.Workspace.Language == language.Go || built.Workspace.ModuleDir != "" || built.Workspace.Root != filepath.Join(cfg.Dir, "web") {
				t.Errorf("Describe().Workspace = %+v, want web built from its own directory and not as go: neither the go.mod at the project root nor its own makes a next app, or one holding no go source, a go app", built.Workspace)
			}
		})
	}
}
