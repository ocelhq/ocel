package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func projectAt(t *testing.T, files ...string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Project{Slug: "shop", Dir: dir, Path: filepath.Join(dir, project.DefaultFileName)}
}

func TestDoctorPassesAProjectThatNamesApps(t *testing.T) {
	t.Parallel()

	cfg := projectAt(t)
	cfg.Apps = []project.App{{Name: "web"}, {Name: "api"}}
	if got := appsCheck(cfg); got.verdict != verdictPass || got.text != "config loads — 2 apps (web, api)" {
		t.Errorf("check = %+v, want the two apps passed", got)
	}
}

func TestDoctorWarnsThatAProjectWithNoAppsAndNoResourcesHasNothingToDeploy(t *testing.T) {
	t.Parallel()

	got := appsCheck(projectAt(t))
	if got.verdict != verdictWarn {
		t.Fatalf("verdict = %v, want a warning: a deploy of this project deploys nothing", got.verdict)
	}
	if want := "config loads — nothing to deploy: no apps declared, none detected at the project root, and no resources declared"; got.text != want {
		t.Errorf("text = %q, want %q", got.text, want)
	}
	if want := `name each app under "apps" in ocel.json with its name and path`; got.fix != want {
		t.Errorf("fix = %q, want %q", got.fix, want)
	}
}

func TestDoctorWarnsWhyNoAppIsDetectedAtTheProjectRoot(t *testing.T) {
	t.Parallel()

	cfg := projectAt(t, discovery.DefaultRootDirName+"/resources.ts")
	cfg.RootAppUndetected = errors.New("no app is detected at the project root, which holds both a python app (main.py) and a Next.js app")
	got := appsCheck(cfg)
	if got.verdict != verdictWarn {
		t.Fatalf("verdict = %v, want a warning", got.verdict)
	}
	if want := "config loads — no apps declared, and no app is detected at the project root, which holds both a python app (main.py) and a Next.js app"; got.text != want {
		t.Errorf("text = %q, want %q", got.text, want)
	}
	if want := `name the app to deploy under "apps" in ocel.json with its name and path`; got.fix != want {
		t.Errorf("fix = %q, want %q", got.fix, want)
	}
}

func TestDoctorPassesAProjectThatDeclaresResourcesAlone(t *testing.T) {
	t.Parallel()

	if got := appsCheck(projectAt(t, discovery.DefaultRootDirName+"/resources.ts")); got.verdict != verdictPass || got.text != "config loads — no apps declared" {
		t.Errorf("check = %+v, want a pass: the resources are what it deploys", got)
	}
}
