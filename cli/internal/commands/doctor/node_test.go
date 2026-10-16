package doctor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/provider"
)

func goProject(t *testing.T, configName string) *project.Project {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"go.mod", configName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Project{Slug: "fixture", Dir: dir, Path: filepath.Join(dir, configName)}
}

func TestAGoProjectNeedsNoNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	if reasons := reasonsFor(cfg); len(reasons) != 0 {
		t.Fatalf("reasonsFor() = %v, want none", reasons)
	}
	if _, applies := checkNode(cfg); applies {
		t.Fatal("a Go project was checked for node")
	}
}

func TestATypeScriptConfigNeedsNodeAndSaysSo(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.config.ts")
	reasons := reasonsFor(cfg)
	if !slices.Contains(reasons, "ocel.config.ts is TypeScript") {
		t.Fatalf("nodeReasons() = %v, want the TypeScript config named", reasons)
	}

	got, applies := checkNode(cfg)
	if !applies {
		t.Fatal("a TypeScript config was not checked for node")
	}
	if !strings.Contains(got.text, "ocel.config.ts is TypeScript") {
		t.Fatalf("check text = %q, want it to name why node is needed", got.text)
	}
}

func TestAJavaScriptAppNeedsNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "apps", "web", "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Apps = []project.App{{Name: "web", Path: "apps/web", Compute: provider.ComputeContainer}}
	if reasons := reasonsFor(cfg); !slices.Contains(reasons, "an app is JavaScript") {
		t.Fatalf("reasonsFor() = %v, want the JavaScript app named", reasons)
	}
}

func TestADeclarationRootWrittenInJavaScriptNeedsNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	dir := filepath.Join(cfg.Dir, discovery.DefaultRootDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte("export {};"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reasons := reasonsFor(cfg); !slices.Contains(reasons, "this project declares resources in JavaScript") {
		t.Fatalf("reasonsFor() = %v, want the JavaScript declarations named", reasons)
	}
}

func TestADiscoveryPathThatIsNotThereNeedsNodeRatherThanPassingSilently(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	cfg.DiscoveryPaths = []string{"nowhere"}
	if reasons := reasonsFor(cfg); !slices.Contains(reasons, "this project declares resources in JavaScript") {
		t.Fatalf("reasonsFor() = %v, want node checked for roots it could not read", reasons)
	}
}

func TestTheFixturesOfAnotherLanguageNeedNoNode(t *testing.T) {
	tried := 0
	for _, dir := range fixturetest.Dirs(t) {
		if fixturetest.IsNode(t, dir) {
			continue
		}
		tried++
		t.Run(filepath.Base(filepath.Dir(dir))+"/"+filepath.Base(dir), func(t *testing.T) {
			if reasons := reasonsFor(&project.Project{Dir: dir}); len(reasons) != 0 {
				t.Fatalf("reasonsFor() = %v for %s, and its own language is not js", reasons, dir)
			}
		})
	}
	if tried == 0 {
		t.Fatal("no fixture is written in a language other than js")
	}
}

func TestConfiguredTransformsNeedNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	cfg.Transforms = []string{"./transforms/default.transform.ts"}
	if reasons := reasonsFor(cfg); !slices.Contains(reasons, "this project lists transforms") {
		t.Fatalf("reasonsFor() = %v, want the transforms named", reasons)
	}

	cfg.Transforms = nil
	if reasons := reasonsFor(cfg); len(reasons) != 0 {
		t.Fatalf("reasonsFor() = %v, want none for an empty transform list", reasons)
	}
}

func TestNodeMissingFromPATHFailsOnlyWhereItIsNeeded(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if _, applies := checkNode(goProject(t, "ocel.json")); applies {
		t.Fatal("a Go project failed over node it never runs")
	}

	got, applies := checkNode(goProject(t, "ocel.config.ts"))
	if !applies || got.verdict != verdictFail {
		t.Fatalf("nodeCheck() = %+v, %v, want a failure naming the missing node", got, applies)
	}
	if !strings.Contains(got.text, "not on PATH") {
		t.Fatalf("check text = %q, want it to say node is not on PATH", got.text)
	}
}

func reasonsFor(cfg *project.Project) []string {
	roots, err := discovery.RootsOf(cfg)
	return nodeReasons(cfg, roots, err)
}

func checkNode(cfg *project.Project) (check, bool) {
	roots, err := discovery.RootsOf(cfg)
	return nodeCheck(context.Background(), cfg, roots, err)
}
