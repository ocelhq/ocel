package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/appbuild"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnAppOnlyItsUsagesNameGetsTheRuntimeItsURLIsWrittenFor(t *testing.T) {
	t.Parallel()
	usages := []attribution.Usage{{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"}}

	t.Run("the node builder's runtime where no function names one", func(t *testing.T) {
		t.Parallel()
		got := appsOf(t.TempDir(), nil, usages, "container", nil, nil)
		if len(got) != 1 || got[0].Framework.Name != appbuild.FrameworkNode {
			t.Errorf("appsOf() = %+v, want web on %q: the CLI writes %s for this project's unnamed app, so the provider must read the same runtime or record ocel's copy as declared", got, appbuild.FrameworkNode, appbuild.ClientURLEnvName)
		}
	})

	t.Run("the runtime its own functions name", func(t *testing.T) {
		t.Parallel()
		functions := []build.Function{{App: "web", Framework: appbuild.Framework{Name: appbuild.FrameworkNext}}}
		got := appsOf(t.TempDir(), nil, usages, "serverless", nil, functions)
		if len(got) != 1 || got[0].Framework.Name != appbuild.FrameworkNext {
			t.Errorf("appsOf() = %+v, want web on %q: a next app keeps the runtime that serves its cache", got, appbuild.FrameworkNext)
		}
	})
}

func TestAnAppOnlyItsUsagesNameTakesTheProvidersDefaultCompute(t *testing.T) {
	t.Parallel()

	got := appsOf(t.TempDir(), nil, []attribution.Usage{
		{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"},
	}, "container", nil, nil)

	if len(got) != 1 || got[0].Compute != "container" {
		t.Errorf("appsOf() = %+v, want the one attributed app with %q", got, "container")
	}
}

func TestTheManifestNamesWhichAppsBundleReadsTheClientURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		app      projectconfig.App
		manifest string
		want     bool
	}{
		{name: "a next app", app: projectconfig.App{Name: "web", Framework: projectconfig.Framework{Name: appbuild.FrameworkNext}}, want: true},
		{name: "a go app", app: projectconfig.App{Name: "api", Framework: projectconfig.Framework{Name: appbuild.FrameworkGo}}, manifest: "go.mod"},
		{name: "a container app containing a package.json", app: projectconfig.App{Name: "store", Compute: string(provider.ComputeContainer)}, manifest: "package.json", want: true},
		{name: "a container app containing a go.mod", app: projectconfig.App{Name: "worker", Compute: string(provider.ComputeContainer)}, manifest: "go.mod"},
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

			got := appsOf(root, []projectconfig.App{tc.app}, nil, "serverless", nil, nil)
			if len(got) != 1 || got[0].ClientBundle != tc.want {
				t.Errorf("appsOf() = %+v, want ClientBundle %v: the provider reads it off the manifest, and a container app has no runtime to read instead", got, tc.want)
			}
		})
	}
}

func TestTheRootResolutionReachesTheAppNothingConfigured(t *testing.T) {
	t.Parallel()

	t.Run("root resolution reaches the app nothing configured", func(t *testing.T) {
		t.Parallel()

		root := []variables.Variable{
			{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-123"},
		}
		functions := []build.Function{{Route: "index", App: "storefront"}}

		got := variablesByApp(map[string][]variables.Variable{variablescope.RootApp: root}, functions)
		if len(got[variablescope.RootApp]) != 0 {
			t.Errorf("variables are still keyed by the placeholder root name: %v", got)
		}
		if len(got["storefront"]) != 1 || got["storefront"][0].Value != "ph-123" {
			t.Fatalf("storefront = %v, want the root resolution", got["storefront"])
		}
	})

	t.Run("configured apps keep their own resolution", func(t *testing.T) {
		t.Parallel()

		resolved := map[string][]variables.Variable{
			"admin":      {{Key: "POSTHOG_ID", Value: "ph-admin"}},
			"storefront": {{Key: "POSTHOG_ID", Value: "ph-store"}},
		}
		functions := []build.Function{{Route: "index", App: "storefront"}}

		got := variablesByApp(resolved, functions)
		if len(got["admin"]) != 1 || got["admin"][0].Value != "ph-admin" {
			t.Fatalf("admin = %v, want its own resolution", got["admin"])
		}
		if got["storefront"][0].Value != "ph-store" {
			t.Fatalf("storefront = %v, want its own resolution", got["storefront"])
		}
	})
}

func TestAConfiguredAppCarriesItsFolderAndOnlyItsOwnUsages(t *testing.T) {
	t.Parallel()

	t.Run("passes the folder binding into the manifest", func(t *testing.T) {
		t.Parallel()

		got := appsOf(t.TempDir(), []projectconfig.App{
			{Name: "admin", Folder: "/admin"},
			{Name: "web"},
		}, nil, "serverless", nil, nil)

		want := []app{
			{Name: "admin", Folder: "/admin"},
			{Name: "web"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("appsOf() = %+v, want %+v", got, want)
		}
	})

	t.Run("hands each app only the usage edges attributed to it", func(t *testing.T) {
		t.Parallel()

		got := appsOf(t.TempDir(), []projectconfig.App{{Name: "admin"}, {Name: "web"}}, []attribution.Usage{
			{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Files: []string{"apps/web/src/server.ts"}},
			{App: "admin", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads", Files: []string{"apps/admin/src/upload.ts"}},
		}, "serverless", nil, nil)

		want := []app{
			{Name: "admin", Usages: []usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads", Files: []string{"apps/admin/src/upload.ts"}}}},
			{Name: "web", Usages: []usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Files: []string{"apps/web/src/server.ts"}}}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("appsOf() = %+v, want %+v", got, want)
		}
	})
}
