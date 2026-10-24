package doctor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
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

	cfg := projectWith(t, `{"slug":"shop","apps":[{"name":"web","path":"web"},{"name":"api","path":"api","framework":"node"},{"name":"jobs","path":"jobs"}]}`, map[string]string{
		"web/package.json": `{"dependencies":{"next":"15"}}`,
	})
	got := checkApps(cfg)
	if got.verdict != verdictPass || got.text != "config loads — 3 apps (web, api, jobs)" {
		t.Errorf("check = %+v, want the three apps passed", got)
	}
	want := []string{"web: next (package.json depends on next)", "api: node (set in ocel.json)", "jobs: no framework detected"}
	if !slices.Equal(got.detail, want) {
		t.Errorf("detail = %q, want %q", got.detail, want)
	}
}

func TestDoctorWarnsThatAProjectWithNoAppsAndNoResourcesHasNothingToDeploy(t *testing.T) {
	t.Parallel()

	got := checkApps(projectWith(t, `{"slug":"shop"}`, map[string]string{"package.json": `{}`}))
	if got.verdict != verdictWarn {
		t.Fatalf("verdict = %v, want a warning: a deploy of this project deploys nothing", got.verdict)
	}
	want := "config loads — nothing to deploy: no apps or resources declared, and the project root holds a package.json with no next dependency or next config, no @sveltejs/kit dependency and no entrypoint among src/server, src/index, src/app, index, server, app or src/main with a .ts, .mts, .cts, .js, .mjs or .cjs extension"
	if got.text != want {
		t.Errorf("text = %q, want %q", got.text, want)
	}
	if want := `name each app under "apps" in ocel.json with its name and path`; got.fix != want {
		t.Errorf("fix = %q, want %q", got.fix, want)
	}
}

func TestDoctorWarnsOfNothingToDeployWhereTheProjectSaysNothingOfItsRoot(t *testing.T) {
	t.Parallel()

	got := appsCheck(&project.Project{Path: project.DefaultFileName}, nil, nil)
	if want := "config loads — nothing to deploy: no apps or resources declared, and no app is detected at the project root"; got.verdict != verdictWarn || got.text != want {
		t.Errorf("check = %+v, want a warning saying %q", got, want)
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
	if want := "config loads — no apps declared, and the project root holds both a Next.js app (package.json depends on next) and a python app (main.py)"; got.text != want {
		t.Errorf("text = %q, want %q", got.text, want)
	}
	if want := `name the app to deploy under "apps" in ocel.json with its name and path`; got.fix != want {
		t.Errorf("fix = %q, want %q", got.fix, want)
	}
}

func TestDoctorWarnsThatTheRootsPackageJSONCannotBeReadWhereTheProjectDeclaresResourcesAlone(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop"}`, map[string]string{"package.json": `{"dependencies":`, "index.ts": "\n", declaredResources: "export {}\n"})
	got := checkApps(cfg)
	if want := "config loads — no apps declared, and the project root holds a package.json that cannot be read"; got.verdict != verdictWarn || !strings.HasPrefix(got.text, want) {
		t.Errorf("check = %+v, want a warning starting %q: the app at the root would otherwise be dropped from the deploy unremarked", got, want)
	}
}

func TestDoctorPassesAProjectThatDeclaresResourcesAloneBesideItsTooling(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop"}`, map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`, declaredResources: "export {}\n"})
	want := "config loads — no apps, only the resources in " + discovery.DefaultRootDirName
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

func TestDoctorNamesTheRootCrateWhereItDeclaresTheResources(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop"}`, map[string]string{
		"Cargo.toml": "[package]\nname = \"shop\"\n\n[dependencies]\nocel-sdk = \"0.1\"\n",
		"src/lib.rs": "\n",
	})
	want := "config loads — no apps, only the resources in the root crate"
	if got := checkApps(cfg); got.verdict != verdictPass || got.text != want {
		t.Errorf("check = %+v, want a pass saying %q", got, want)
	}
}

func TestDoctorNamesTheProjectRootWhereItIsTheDiscoveryFolder(t *testing.T) {
	t.Parallel()

	cfg := projectWith(t, `{"slug":"shop","discovery":{"paths":["."]}}`, map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`, "resources.ts": "export {}\n"})
	want := "config loads — no apps, only the resources in the project root"
	if got := checkApps(cfg); got.verdict != verdictPass || got.text != want {
		t.Errorf("check = %+v, want a pass saying %q", got, want)
	}
}
