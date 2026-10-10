package manifest

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestAConfiguredAppCarriesItsFolderAndOnlyItsOwnUsages(t *testing.T) {
	t.Parallel()

	t.Run("passes the folder binding into the manifest", func(t *testing.T) {
		t.Parallel()

		got := appsOf([]project.App{
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

		got := appsOf([]project.App{{Name: "admin"}, {Name: "web"}}, []attribution.Usage{
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
