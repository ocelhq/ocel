package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnAppOnlyItsUsagesNameCarriesTheRuntimeItsURLIsWrittenFor(t *testing.T) {
	t.Parallel()
	usages := []attribution.Usage{{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"}}

	t.Run("the node builder's runtime where no function names one", func(t *testing.T) {
		t.Parallel()
		got := toApps(nil, usages, "container", nil, nil)
		if len(got) != 1 || got[0].Runtime.Name != providerkit.RuntimeNode {
			t.Errorf("toApps() = %+v, want web on %q: the CLI writes %s for this project's unnamed app, so the provider must read the same runtime or record ocel's copy as declared", got, providerkit.RuntimeNode, providerkit.ClientURLEnvName)
		}
	})

	t.Run("the runtime its own functions name", func(t *testing.T) {
		t.Parallel()
		functions := []manifestbuilder.Function{{App: "web", Runtime: manifestbuilder.Runtime{Name: providerkit.RuntimeNext}}}
		got := toApps(nil, usages, "serverless", nil, functions)
		if len(got) != 1 || got[0].Runtime.Name != providerkit.RuntimeNext {
			t.Errorf("toApps() = %+v, want web on %q: a next app keeps the runtime that serves its cache", got, providerkit.RuntimeNext)
		}
	})
}

func TestTheManifestCarriesEveryAppsCompute(t *testing.T) {
	t.Run("an app the config names carries the compute resolved onto it", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		s, _ := newBuildManifestSession(t)
		cfg := &projectconfig.Config{
			Dir:  root,
			Slug: "prebuilt",
			Apps: []projectconfig.App{{Name: "api", Path: ".", Compute: "container"}},
		}
		clitest.StubAppImages(&deps, "api")
		manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "serverless", nil)
		if err != nil {
			t.Fatalf("collectAndBuildManifest: %v", err)
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

		s, _ := newBuildManifestSession(t)
		cfg := &projectconfig.Config{Dir: root, Slug: "prebuilt"}
		_, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container", nil)
		if err == nil {
			t.Fatal("collectAndBuildManifest() landed an app the config never names on container compute, so a provider would be handed an app with no image")
		}
		if !strings.Contains(err.Error(), `"api"`) {
			t.Errorf("collectAndBuildManifest() error = %q, want it to name the app", err)
		}
	})
}

func TestAnAppOnlyItsUsagesNameTakesTheProvidersDefaultCompute(t *testing.T) {
	t.Parallel()

	got := toApps(nil, []attribution.Usage{
		{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"},
	}, "container", nil, nil)

	if len(got) != 1 || got[0].Compute != "container" {
		t.Errorf("toApps() = %+v, want the one attributed app carrying %q", got, "container")
	}
}

func computeOf(t *testing.T, manifest *contractv1.Manifest, app string) string {
	t.Helper()
	for _, candidate := range manifest.GetApps() {
		if candidate.GetName() == app {
			return candidate.GetCompute()
		}
	}
	t.Fatalf("manifest carries no app %q: %+v", app, manifest.GetApps())
	return ""
}

func TestAContainerAppThatNamesNoRuntimeStillReachesTheProvider(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	recordBuildApp(&deps)

	s, _ := newBuildManifestSession(t)
	cfg := &projectconfig.Config{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []projectconfig.App{{Name: "api", Path: ".", Compute: "container"}},
	}
	clitest.StubAppImages(&deps, "api")

	manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container", nil)
	if err != nil {
		t.Fatalf("collectAndBuildManifest over a container app with no runtime: %v", err)
	}
	if got := computeOf(t, manifest, "api"); got != "container" {
		t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
	}
}
