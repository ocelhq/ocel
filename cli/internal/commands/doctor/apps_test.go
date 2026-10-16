package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func projectWith(t *testing.T, config string, files map[string]string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	files[project.DefaultFileName] = config
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := project.Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func checkApps(cfg *project.Project) check {
	roots, err := discovery.RootsOf(cfg)
	return appsCheck(cfg, roots, err)
}

const declaredResources = discovery.DefaultRootDirName + "/resources.ts"

func TestDoctorPassesAProjectThatNamesApps(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop","apps":[{"name":"web","path":"web"},{"name":"api","path":"api"}]}`, map[string]string{})
	if got := checkApps(cfg); got.verdict != verdictPass || got.text != "config loads — 2 apps (web, api)" {
		t.Errorf("check = %+v, want the two apps passed", got)
	}
}

func TestDoctorWarnsThatAProjectWithNoAppsAndNoResourcesHasNothingToDeploy(t *testing.T) {
	t.Parallel()

	got := checkApps(projectWith(t, `{"slug":"shop"}`, map[string]string{"package.json": `{}`}))
	if got.verdict != verdictWarn {
		t.Fatalf("verdict = %v, want a warning: a deploy of this project deploys nothing", got.verdict)
	}
	want := "config loads — nothing to deploy: no apps or resources declared, and no app is detected at the project root, which holds a package.json with no next or @sveltejs/kit dependency, no start script, no main and no entrypoint among " + strings.Join(language.NodeEntrypoints, ", ") + ", index.mjs, index.cjs"
	if got.text != want {
		t.Errorf("text = %q, want %q", got.text, want)
	}
	if want := `name each app under "apps" in ocel.json with its name and path`; got.fix != want {
		t.Errorf("fix = %q, want %q", got.fix, want)
	}
}

func TestDoctorWarnsWhyNoAppIsDetectedAtARootHoldingTwoApps(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop"}`, map[string]string{
		"package.json":    `{"dependencies":{"next":"15"}}`,
		"pyproject.toml":  "[project]\nname = \"api\"\n",
		"main.py":         "\n",
		declaredResources: "export {}\n",
	})
	got := checkApps(cfg)
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

func TestDoctorPassesAProjectThatDeclaresResourcesAloneBesideItsTooling(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop"}`, map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`, declaredResources: "export {}\n"})
	want := "config loads — no apps declared, so a deploy carries only the resources declared in " + discovery.DefaultRootDirName
	if got := checkApps(cfg); got.verdict != verdictPass || got.text != want {
		t.Errorf("check = %+v, want a pass saying %q: the resources are what it deploys", got, want)
	}
}

func TestDoctorFailsAProjectWhoseDiscoveryFolderADeployCouldNotRead(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop","discovery":{"paths":["nowhere"]}}`, map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`})
	got := checkApps(cfg)
	if got.verdict != verdictFail || !strings.Contains(got.text, `the configured path "nowhere" does not exist`) {
		t.Errorf("check = %+v, want a failure naming the missing discovery path", got)
	}
}
