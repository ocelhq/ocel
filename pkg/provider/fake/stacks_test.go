package fake_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestFakeStacksNameAResourceForEveryBindingFunctionAndContainerTheyProvisioned(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks := fake.NewStacks(fake.NewArtifacts())
	infra := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	web := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("d1", "f1"))}
	worker := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "worker", naming.NewReleaseToken("d1", "f1"))}

	if _, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:       infra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Type: provider.BindingBucket, Name: "uploads"}},
	}, nil); err != nil {
		t.Fatalf("Provision(infra) = %v", err)
	}
	artifact := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifact, "index.js"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:  web,
		Kind: provider.StackApp,
		App:  &provider.AppSpec{App: "web", Compute: provider.ComputeServerless, Functions: []provider.FunctionSpec{{Name: "api", ArtifactDir: artifact}}},
	}, nil); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	if _, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:  worker,
		Kind: provider.StackApp,
		App:  &provider.AppSpec{App: "worker", Compute: provider.ComputeContainer, Image: "ocel/worker:1", HealthCheckPath: "/"},
	}, nil); err != nil {
		t.Fatalf("Provision(worker) = %v", err)
	}

	for ref, want := range map[provider.StackRef][]provider.PhysicalResource{
		infra:  {{Kind: "fake:bucket", ID: "uploads"}},
		web:    {{Kind: "fake:function", ID: web.Name.String() + "-api", ConsoleURL: "https://console.fake.example/" + web.Name.String() + "-api"}},
		worker: {{Kind: "fake:container", ID: worker.Name.String() + "-worker"}},
	} {
		listed, err := stacks.Resources(ctx, provider.StackResources{Ref: ref})
		if err != nil {
			t.Fatalf("Resources(%s) = %v", ref.Name, err)
		}
		if !slices.Equal(listed, want) {
			t.Errorf("Resources(%s) = %v, want %v", ref.Name, listed, want)
		}
	}

	unknown := provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-1")}
	if listed, err := stacks.Resources(ctx, provider.StackResources{Ref: unknown}); err != nil || listed != nil {
		t.Errorf("Resources() of a stack never provisioned = %v, %v, want none", listed, err)
	}
}

func TestFakeStacksNameAResourceForEveryBindingFunctionAndContainerAStackRecordHolds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	release := naming.NewReleaseToken("d1", "f1")
	web := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", release)}
	if err := stackrecords.Write(ctx, vendor.KeyValues(), web.Tier, web.Project, web.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		App:        "web",
		Bindings:   []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Resource: "shop-orders"}},
		Functions:  []provider.Function{{Name: "api", Physical: "shop-web-api"}},
		Containers: []provider.AppContainer{{Name: "worker"}},
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := vendor.Stacks().Resources(ctx, provider.StackResources{Ref: web})
	if err != nil {
		t.Fatalf("Resources() = %v", err)
	}
	want := []provider.PhysicalResource{
		{Kind: "fake:postgres", ID: "shop-orders"},
		{Kind: "fake:function", ID: "shop-web-api", ConsoleURL: "https://console.fake.example/shop-web-api"},
		{Kind: "fake:container", ID: "worker"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("Resources() of a recorded stack = %v, want %v", listed, want)
	}

	unknown := provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-1")}
	if listed, err := vendor.Stacks().Resources(ctx, provider.StackResources{Ref: unknown}); err != nil || listed != nil {
		t.Errorf("Resources() of a stack neither provisioned nor recorded = %v, %v, want none", listed, err)
	}
}
