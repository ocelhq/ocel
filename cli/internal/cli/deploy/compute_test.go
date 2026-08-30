package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

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
			Apps: []projectconfig.App{{Name: "api", Path: ".", Framework: "express", Compute: "container"}},
		}
		manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "serverless")
		if err != nil {
			t.Fatalf("collectAndBuildManifest: %v", err)
		}
		if got := computeOf(t, manifest, "api"); got != "container" {
			t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
		}
	})

	t.Run("an app only the build names carries the compute the provider defaults to", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		s, _ := newBuildManifestSession(t)
		cfg := &projectconfig.Config{Dir: root, Slug: "prebuilt"}
		manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container")
		if err != nil {
			t.Fatalf("collectAndBuildManifest: %v", err)
		}
		if got := computeOf(t, manifest, "api"); got != "container" {
			t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
		}
	})
}

func TestAnAppOnlyItsUsagesNameTakesTheProvidersDefaultCompute(t *testing.T) {
	t.Parallel()

	got := toApps(nil, []attribution.Usage{
		{App: "web", Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"},
	}, "container")

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

func TestAContainerAppThatNamesNoFrameworkStillReachesTheProvider(t *testing.T) {
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

	manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container")
	if err != nil {
		t.Fatalf("collectAndBuildManifest over a container app with no framework: %v", err)
	}
	if got := computeOf(t, manifest, "api"); got != "container" {
		t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
	}
}
