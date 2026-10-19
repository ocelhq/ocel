package manifest

import (
	"maps"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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

func TestEachProductionHostnameIsAttributedToTheAppTheDeployManifestServesItOn(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Slug:    "shop",
		Dir:     t.TempDir(),
		Domains: project.Domains{Production: []string{"shop.example", "api.shop.example"}},
		Apps: []project.App{
			{Name: "web", Path: "apps/web", Compute: "serverless"},
			{Name: "api", Path: "apps/api", Compute: "serverless", ProductionDomains: []string{"api.shop.example"}},
		},
	}
	deployed, err := Assemble(Input{
		Project: cfg,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Built: build.Output{Functions: []build.Function{
			{App: "web", Route: "server", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/server.func"},
			{App: "api", Route: "server", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/api/functions/server.func"},
		}},
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	own := make([][]string, len(deployed.GetApps()))
	for slot, app := range deployed.GetApps() {
		own[slot] = productionHostnamesOf(app.GetDomains())
	}
	want := map[string]string{}
	for slot, hosts := range edge.AttributeHostnames(productionHostnamesOf(deployed.GetDomains()), own) {
		for _, host := range hosts {
			want[host] = deployed.GetApps()[slot].GetName()
		}
	}

	if got := AttributeProductionHostnames(cfg); !maps.Equal(got, want) {
		t.Errorf("AttributeProductionHostnames() = %v, want %v, the app the deploy serves each hostname on from the manifest it is handed", got, want)
	}
}

func productionHostnamesOf(declared []*contractv1.TierDomains) []string {
	var hosts []string
	for _, domains := range declared {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			hosts = append(hosts, domains.GetHostnames()...)
		}
	}
	return hosts
}
