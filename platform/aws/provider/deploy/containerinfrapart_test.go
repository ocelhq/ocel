package deploy

import (
	"context"
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestTheContainerInfraIsAPartAddedByTheFirstContainerDeployAndUsedByEveryOneAfter(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	stacks := stacksWith(cfg, &mockedEngine{outputs: outputs})
	ctx := context.Background()
	tier := environment.TierProduction

	if _, present, err := ReadContainerInfrastructurePart(ctx, cfg.KeyValues, "us-east-1", tier); err != nil || present {
		t.Fatalf("ReadContainerInfrastructurePart() before any container deploy = %v, %v, want none", present, err)
	}
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	blog := spec
	blog.Ref = provider.StackRef{Project: "blog", Tier: tier, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	if _, err := stacks.Provision(ctx, blog, progress.Discard()); err != nil {
		t.Fatalf("Provision(blog) = %v", err)
	}

	part, present, err := ReadContainerInfrastructurePart(ctx, cfg.KeyValues, "us-east-1", tier)
	if err != nil || !present {
		t.Fatalf("ReadContainerInfrastructurePart() = %v, %v, want the container infrastructure", present, err)
	}
	if !slices.Equal(part.UsedBy, []string{"blog", spec.Ref.Project}) {
		t.Errorf("containers used by %v, want both container projects", part.UsedBy)
	}
	if len(part.Resources) == 0 || part.Shape == nil || len(part.Shape.Resources) == 0 {
		t.Errorf("containers = %+v, want its resources and the shape that prices them", part)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, tier, ContainersPart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != spec.Ref.Project {
		t.Errorf("containers record = %+v, %v, %v, want it ready and added by %s", recorded, present, err, spec.Ref.Project)
	}

	for _, ref := range []provider.StackRef{spec.Ref, blog.Ref} {
		if err := stacks.Destroy(ctx, ref, nil, progress.Discard()); err != nil {
			t.Fatalf("Destroy(%s) = %v", ref.Project, err)
		}
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, tier, ContainersPart); err != nil || present {
		t.Errorf("containers record present %v (%v) after its last user left, want it forgotten with the infrastructure", present, err)
	}
}
