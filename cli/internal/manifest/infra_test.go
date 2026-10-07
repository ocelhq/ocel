package manifest

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestTheInfraManifestDeclaresTheResourcesAndWorkersOfTheDeployManifestAndNoApps(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Slug:    "shop",
		Dir:     t.TempDir(),
		Domains: project.Domains{Production: []string{"shop.example"}},
		Apps:    []project.App{{Name: "web", Path: "apps/web", Compute: "serverless"}},
	}
	resources := []declaration.Resource{
		{Name: "orders", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "apps/web/src/db.ts:1"},
		{Name: "media", Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Worker: &resourcesv1.WorkerConfig{}, Source: "apps/web/src/media.ts:1"},
		{Name: "signups", Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Topic: &resourcesv1.TopicConfig{}, Source: "apps/web/src/signups.ts:1"},
		{Name: "welcome", Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Consumer: &resourcesv1.ConsumerConfig{Topic: "signups", Worker: "media"}, Source: "apps/web/src/welcome.ts:1"},
	}

	infra, err := AssembleInfra(InfraInput{Project: cfg, Tier: environmentv1.Tier_TIER_PRODUCTION, Resources: resources})
	if err != nil {
		t.Fatalf("AssembleInfra() error = %v", err)
	}
	deployed, err := Assemble(Input{
		Project:   cfg,
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Resources: resources,
		Built: build.Output{Functions: []build.Function{{
			App: "web", Route: "server", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/server.func",
		}}},
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}

	if len(infra.GetApps()) != 0 || len(infra.GetUsages()) != 0 {
		t.Errorf("the infra manifest declares %d apps and %d usages, want none: apps ship once they are built", len(infra.GetApps()), len(infra.GetUsages()))
	}
	infraHalf := &contractv1.Manifest{Slug: infra.GetSlug(), Resources: infra.GetResources(), Domains: infra.GetDomains(), Workers: infra.GetWorkers()}
	deployHalf := &contractv1.Manifest{Slug: deployed.GetSlug(), Resources: deployed.GetResources(), Domains: deployed.GetDomains(), Workers: deployed.GetWorkers()}
	if !proto.Equal(infraHalf, deployHalf) {
		t.Errorf("the infra manifest declares resources %v and workers %v, want what the deploy manifest declares, %v and %v: the provider checks the provisioned infra against it",
			logicalNames(infra.GetResources()), workerNames(infra), logicalNames(deployed.GetResources()), workerNames(deployed))
	}
}

func TestTheInfraManifestOfAContainerAppIsAssembledBeforeItsImageIsBuilt(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Slug: "shop",
		Dir:  t.TempDir(),
		Apps: []project.App{{Name: "api", Path: "apps/api", Compute: "container", Container: &project.Container{}}},
	}
	resources := []declaration.Resource{
		{Name: "orders", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "apps/api/src/db.ts:1"},
	}

	infra, err := AssembleInfra(InfraInput{Project: cfg, Tier: environmentv1.Tier_TIER_PRODUCTION, Resources: resources})
	if err != nil {
		t.Fatalf("AssembleInfra() error = %v, want the infra manifest: it provisions before the image the app runs is built", err)
	}
	if len(infra.GetResources()) != 1 {
		t.Errorf("the infra manifest declares %d resources, want orders", len(infra.GetResources()))
	}
}

func TestTheInfraManifestServesTheProductionDomainsDeclaredOnItsApps(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Slug:    "shop",
		Dir:     t.TempDir(),
		Domains: project.Domains{Production: []string{"shop.example"}},
		Apps:    []project.App{{Name: "api", Path: "apps/api", Compute: "serverless", ProductionDomains: []string{"api.shop.example"}}},
	}
	resources := []declaration.Resource{
		{Name: "orders", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "apps/api/src/db.ts:1"},
	}

	infra, err := AssembleInfra(InfraInput{Project: cfg, Tier: environmentv1.Tier_TIER_PRODUCTION, Resources: resources})
	if err != nil {
		t.Fatalf("AssembleInfra() error = %v", err)
	}
	var served []string
	for _, domains := range infra.GetDomains() {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			served = append(served, domains.GetHostnames()...)
		}
	}
	if want := []string{"shop.example", "api.shop.example"}; !slices.Equal(served, want) {
		t.Errorf("the infra manifest serves production on %v, want %v: the provider refuses infra for a production deploy with nowhere to serve, and the app that declares api.shop.example is not in it",
			served, want)
	}
	if got := cfg.Domains.Production; !slices.Equal(got, []string{"shop.example"}) {
		t.Errorf("AssembleInfra() changed the project's production domains to %v", got)
	}
}

func logicalNames(resources []*contractv1.ManifestResource) []string {
	names := make([]string, 0, len(resources))
	for _, resource := range resources {
		names = append(names, resource.GetLogicalName())
	}
	return names
}
