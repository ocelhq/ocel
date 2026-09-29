package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTheManifestNamesEveryAppsCompute(t *testing.T) {
	t.Run("an app the config names has the compute resolved onto it", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		s, _ := newBuildSpan(t)
		cfg := &project.Project{
			Dir:  root,
			Slug: "prebuilt",
			Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
		}
		clitest.StubAppImages(&deps, "api")
		manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if got := computeOf(t, manifest, "api"); got != "container" {
			t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
		}
	})

	t.Run("an app only the build names cannot land on the provider's container default", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		s, _ := newBuildSpan(t)
		cfg := &project.Project{Dir: root, Slug: "prebuilt"}
		_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble() landed an app the config never names on container compute, so a provider would be handed an app with no image")
		}
		if !strings.Contains(err.Error(), `"api"`) {
			t.Errorf("collectBuildAndAssemble() error = %q, want it to name the app", err)
		}
	})
}

func onCompute(cfg *project.Project, compute provider.Compute) *project.Project {
	resolved := *cfg
	resolved.Apps = make([]project.App, len(cfg.Apps))
	for i, app := range cfg.Apps {
		if app.Compute == "" {
			app.Compute = compute
		}
		resolved.Apps[i] = app
	}
	return &resolved
}

func computeOf(t *testing.T, manifest *contractv1.Manifest, app string) string {
	t.Helper()
	for _, candidate := range manifest.GetApps() {
		if candidate.GetName() == app {
			return string(provider.ComputeOf(candidate))
		}
	}
	names := make([]string, 0, len(manifest.GetApps()))
	for _, candidate := range manifest.GetApps() {
		names = append(names, candidate.GetName())
	}
	t.Fatalf("manifest has no app %q among its apps %q", app, names)
	return ""
}

func TestAContainerAppThatNamesNoRuntimeStillReachesTheProvider(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	recordBuildApp(&deps)

	s, _ := newBuildSpan(t)
	cfg := &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
	}
	clitest.StubAppImages(&deps, "api")

	manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble over a container app with no runtime: %v", err)
	}
	if got := computeOf(t, manifest, "api"); got != "container" {
		t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
	}
}
