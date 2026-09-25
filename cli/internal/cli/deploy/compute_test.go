package deploy

import (
	"context"
	"os"
	"path/filepath"
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
		got := toApps(t.TempDir(), nil, usages, "container", nil, nil)
		if len(got) != 1 || got[0].Framework.Name != providerkit.FrameworkNode {
			t.Errorf("toApps() = %+v, want web on %q: the CLI writes %s for this project's unnamed app, so the provider must read the same runtime or record ocel's copy as declared", got, providerkit.FrameworkNode, providerkit.ClientURLEnvName)
		}
	})

	t.Run("the runtime its own functions name", func(t *testing.T) {
		t.Parallel()
		functions := []manifestbuilder.Function{{App: "web", Framework: manifestbuilder.Framework{Name: providerkit.FrameworkNext}}}
		got := toApps(t.TempDir(), nil, usages, "serverless", nil, functions)
		if len(got) != 1 || got[0].Framework.Name != providerkit.FrameworkNext {
			t.Errorf("toApps() = %+v, want web on %q: a next app keeps the runtime that serves its cache", got, providerkit.FrameworkNext)
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
		manifest, _, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "serverless", nil, nil)
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
		_, _, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container", nil, nil)
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

	got := toApps(t.TempDir(), nil, []attribution.Usage{
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
	names := make([]string, 0, len(manifest.GetApps()))
	for _, candidate := range manifest.GetApps() {
		names = append(names, candidate.GetName())
	}
	t.Fatalf("manifest carries no app %q among its apps %q", app, names)
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

	manifest, _, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "container", nil, nil)
	if err != nil {
		t.Fatalf("collectAndBuildManifest over a container app with no runtime: %v", err)
	}
	if got := computeOf(t, manifest, "api"); got != "container" {
		t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
	}
}

func TestTheManifestCarriesWhichAppsBundleReadsTheClientURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		app      projectconfig.App
		manifest string
		want     bool
	}{
		{name: "a next app", app: projectconfig.App{Name: "web", Framework: projectconfig.Framework{Name: providerkit.FrameworkNext}}, want: true},
		{name: "a go app", app: projectconfig.App{Name: "api", Framework: projectconfig.Framework{Name: providerkit.FrameworkGo}}, manifest: "go.mod"},
		{name: "a container app holding a package.json", app: projectconfig.App{Name: "store", Compute: string(providerkit.ComputeContainer)}, manifest: "package.json", want: true},
		{name: "a container app holding a go.mod", app: projectconfig.App{Name: "worker", Compute: string(providerkit.ComputeContainer)}, manifest: "go.mod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tc.app.Path = tc.app.Name
			dir := filepath.Join(root, tc.app.Path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("create %s: %v", dir, err)
			}
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.manifest), nil, 0o644); err != nil {
					t.Fatalf("write %s: %v", tc.manifest, err)
				}
			}

			got := toApps(root, []projectconfig.App{tc.app}, nil, "serverless", nil, nil)
			if len(got) != 1 || got[0].ClientBundle != tc.want {
				t.Errorf("toApps() = %+v, want ClientBundle %v: the provider reads it off the manifest, and a container app carries no runtime to read instead", got, tc.want)
			}
		})
	}
}
