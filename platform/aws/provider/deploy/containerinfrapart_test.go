package deploy

import (
	"context"
	"errors"
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

func TestTheContainerInfrastructureIsAPartAddedByTheFirstContainerDeployAndUsedByEveryOneAfter(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	stacks := stacksWith(cfg, &mockedEngine{outputs: containerPartOutputs()})
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
	if len(part.Resources) == 0 || part.Shape == nil || len(part.Shape.Resources) == 0 || !part.RetiresWithLastUser {
		t.Errorf("containers = %+v, want its resources, the shape that prices them, and it retiring with its last user", part)
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

func TestAnOlderReleasesContainerDeployLeavesTheContainersPartANewerReleaseRecorded(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	ctx := context.Background()
	tier := environment.TierProduction
	newer := stackrecords.BootstrapPart{WrittenBy: "v2.0.0", AddedBy: "blog"}
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, cfg.KeyValues, tier, ContainersPart, newer); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.MarkBootstrapPartReady(ctx, cfg.KeyValues, tier, ContainersPart, newer); err != nil {
		t.Fatal(err)
	}

	if _, err := stacksWith(cfg, &mockedEngine{outputs: containerPartOutputs()}).Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}

	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, tier, ContainersPart)
	if err != nil || recorded.WrittenBy != "v2.0.0" {
		t.Errorf("containers record written by %q (%v), want the newer release's v2.0.0 kept", recorded.WrittenBy, err)
	}
}

func TestAContainerDeployRecordsTheContainersPartAsWrittenByItsOwnRelease(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	ctx := context.Background()

	if _, err := stacksWith(cfg, &mockedEngine{outputs: containerPartOutputs()}).Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}

	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, environment.TierProduction, ContainersPart)
	if err != nil || recorded.WrittenBy != "v1.0.0" {
		t.Errorf("containers record written by %q (%v), want the deploying release v1.0.0", recorded.WrittenBy, err)
	}
}

func TestAContainerDeployWhoseInfrastructureFailsLeavesNoContainersPartCreating(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	ctx := context.Background()
	infra := containerInfraRef(environment.TierProduction).Name.String()
	engine := &mockedEngine{outputs: containerPartOutputs(), upErr: func(stack string) error {
		if stack == infra {
			return errors.New("the load balancer quota is spent")
		}
		return nil
	}}

	if _, err := stacksWith(cfg, engine).Provision(ctx, spec, progress.Discard()); err == nil {
		t.Fatal("Provision(shop) succeeded, want the failed container infrastructure to fail it")
	}

	if recorded, present, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, environment.TierProduction, ContainersPart); err != nil || present {
		t.Errorf("containers record = %+v, %v, %v after its infrastructure failed, want no claim left creating", recorded, present, err)
	}
}

func containerPartSpec(t *testing.T, writer provider.WrittenBy) (Config, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	cfg.WrittenBy = writer
	return cfg, spec
}

func containerPartOutputs() map[string]auto.OutputValue {
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	return outputs
}
