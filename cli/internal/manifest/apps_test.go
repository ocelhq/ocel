package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTheManifestNamesWhichAppsBundleReadsTheClientURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		app      project.App
		manifest string
		want     bool
	}{
		{name: "a next app", app: project.App{Name: "web", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}}, want: true},
		{name: "a go app", app: project.App{Name: "api", Serverless: &project.Serverless{Framework: buildoutput.FrameworkGo}}, manifest: "go.mod"},
		{name: "a container app containing a package.json", app: project.App{Name: "store", Compute: provider.ComputeContainer}, manifest: "package.json", want: true},
		{name: "a container app containing a go.mod", app: project.App{Name: "worker", Compute: provider.ComputeContainer}, manifest: "go.mod"},
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

			got := appsOf(root, []project.App{tc.app}, nil, nil)
			if len(got) != 1 || got[0].ClientBundle != tc.want {
				t.Errorf("appsOf() = %+v, want ClientBundle %v: the provider reads it off the manifest, and a container app has no runtime to read instead", got, tc.want)
			}
		})
	}
}

func TestAConfiguredAppCarriesItsFolderAndOnlyItsOwnUsages(t *testing.T) {
	t.Parallel()

	t.Run("passes the folder binding into the manifest", func(t *testing.T) {
		t.Parallel()

		got := appsOf(t.TempDir(), []project.App{
			{Name: "admin", Folder: "/admin"},
			{Name: "web"},
		}, nil, nil)

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

		got := appsOf(t.TempDir(), []project.App{{Name: "admin"}, {Name: "web"}}, []attribution.Usage{
			{App: "web", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Files: []string{"apps/web/src/server.ts"}},
			{App: "admin", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads", Files: []string{"apps/admin/src/upload.ts"}},
		}, nil)

		want := []app{
			{Name: "admin", Usages: []usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads", Files: []string{"apps/admin/src/upload.ts"}}}},
			{Name: "web", Usages: []usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Files: []string{"apps/web/src/server.ts"}}}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("appsOf() = %+v, want %+v", got, want)
		}
	})
}
