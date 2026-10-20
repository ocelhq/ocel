package deploy

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

func containerInfraOutputs() auto.OutputMap {
	infra := fixtureContainerInfra()
	subnets, _ := json.Marshal(infra.Subnets)
	return auto.OutputMap{
		"vpcId":                auto.OutputValue{Value: infra.VPC},
		"subnets":              auto.OutputValue{Value: string(subnets)},
		"listenerArn":          auto.OutputValue{Value: infra.Listener},
		"originHost":           auto.OutputValue{Value: infra.OriginHost},
		"vpcOrigin":            auto.OutputValue{Value: infra.VPCOrigin},
		"taskSecurityGroup":    auto.OutputValue{Value: infra.TaskSecurity},
		"clientsSecurityGroup": auto.OutputValue{Value: infra.Clients},
		"cluster":              auto.OutputValue{Value: infra.Cluster},
		"executionRoleArn":     auto.OutputValue{Value: infra.ExecutionRole},
		"logGroup":             auto.OutputValue{Value: infra.LogGroup},
	}
}

func TestTheFirstContainerDeployProvisionsTheContainerInfraAndTheLastTakesItDown(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		container.OutputKeyURL:      "http://" + fixtureOrigin,
		container.OutputKeyPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	engine := &mockedEngine{outputs: outputs}
	stacks := stacksWith(cfg, engine)
	ctx := context.Background()

	shop := spec
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	ran := engine.stacks()
	if len(ran) != 2 || ran[0] != containers.StackRef(environment.TierProduction).Name.String() || ran[1] != shop.Ref.Name.String() {
		t.Fatalf("the first container deploy ran %v, want the container infrastructure stack before the app stack", ran)
	}
	front, recorded, err := awsports.ReadContainerFront(ctx, cfg.KeyValues, environment.TierProduction)
	if err != nil || !recorded {
		t.Fatalf("the front is recorded %v (%v), want the edge able to read which VPC origin reaches the tier's containers", recorded, err)
	}
	if front != (awsports.ContainerFront{VPCOrigin: fixtureFront, Host: fixtureOrigin}) {
		t.Errorf("front = %+v, want the shared container infrastructure's VPC origin and host", front)
	}

	blog := spec
	blog.Ref = provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	if _, err := stacks.Provision(ctx, blog, progress.Discard()); err != nil {
		t.Fatalf("Provision(blog) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 3 {
		t.Fatalf("the second container deploy ran %v, want the shared container infrastructure reused rather than provisioned again", ran)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: shop.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if destroyed := engine.torn(); len(destroyed) != 1 {
		t.Fatalf("destroying one of two container stacks tore down %v, want only its own: the other still answers behind the front", destroyed)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: blog.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(blog) = %v", err)
	}
	destroyed := engine.torn()
	if len(destroyed) != 3 || destroyed[2] != containers.StackRef(environment.TierProduction).Name.String() {
		t.Fatalf("destroying the last container stack tore down %v, want the shared container infrastructure to go with it: nothing idle-billing survives the last container", destroyed)
	}
	if _, present, err := stackrecords.Read(ctx, cfg.KeyValues, environment.TierProduction, containers.Slug, containers.StackRef(environment.TierProduction).Name); err != nil || present {
		t.Errorf("the shared container infrastructure is still recorded (present %v, err %v) after its last consumer left", present, err)
	}
	if _, recorded, err := awsports.ReadContainerFront(ctx, cfg.KeyValues, environment.TierProduction); err != nil || recorded {
		t.Errorf("the front is still recorded (%v, err %v) after the shared container infrastructure went; a later promote would declare an origin that no longer exists", recorded, err)
	}
}

func TestAContainerDeployThatFailsLeavesNoConsumerBehindAndKeepsTheContainerInfraItRaised(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	engine := &mockedEngine{outputs: containerInfraOutputs()}
	stacks := stacksWith(cfg, engine)
	ctx := context.Background()

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err == nil {
		t.Fatal("Provision succeeded with no container output, so a deploy would record a container with no origin")
	}
	remaining, err := listConsumers(ctx, cfg.KeyValues)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("a failed deploy left %d consumer records, so the shared container infrastructure could never be taken down", len(remaining))
	}
	if torn := engine.torn(); len(torn) != 1 || torn[0] != spec.Ref.Name.String() {
		t.Errorf("after the only consumer failed the engine tore down %v, want only the half-built app stack: the retry converges on the container infrastructure rather than raising it again", torn)
	}
	requireContainerInfraRecorded(t, cfg.KeyValues)

	spec.App.HealthCheckPath = "not a path"
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err == nil {
		t.Fatal("Provision accepted a health check path a load balancer cannot probe")
	}
	if ran := engine.stacks(); len(ran) != 2 {
		t.Errorf("a refused deploy ran %v, want no second container infrastructure stack: the app is checked before anything is provisioned", ran)
	}
}

func TestReclaimingAFailedDeploysReleaseKeepsTheContainerInfraItRaisedAndTheRetryReusesIt(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	engine := &mockedEngine{outputs: containerPartOutputs()}
	stacks := stacksWith(cfg, engine)
	ctx := context.Background()
	tier := environment.TierProduction
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(the failing deploy's release) = %v", err)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref, KeepParts: true}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}
	if torn := engine.torn(); len(torn) != 1 || torn[0] != spec.Ref.Name.String() {
		t.Fatalf("reclaiming the failed deploy's release tore down %v, want only its own stack: the retry would spend minutes raising the container infrastructure again", torn)
	}
	requireContainerInfraRecorded(t, cfg.KeyValues)
	if remaining, err := listConsumers(ctx, cfg.KeyValues); err != nil || len(remaining) != 0 {
		t.Errorf("consumers = %v (%v) after the failed release went, want none: a claim naming a stack that is gone would refuse the part's removal forever", remaining, err)
	}
	part, present, err := containers.ReadPart(ctx, cfg.KeyValues, "ocel", "us-east-1", tier)
	if err != nil || !present || len(part.UsedBy) != 0 {
		t.Errorf("containers.ReadPart() = %+v, %v, %v, want the container infrastructure listed and used by no project", part, present, err)
	}

	retry := spec
	retry.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	if _, err := stacks.Provision(ctx, retry, progress.Discard()); err != nil {
		t.Fatalf("Provision(the retry) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 3 || ran[2] != retry.Ref.Name.String() {
		t.Errorf("the retry ran %v, want only its own app stack: the container infrastructure the failed deploy raised is still there", ran)
	}
	if part, _, err := containers.ReadPart(ctx, cfg.KeyValues, "ocel", "us-east-1", tier); err != nil || !slices.Equal(part.UsedBy, []string{spec.Ref.Project}) {
		t.Errorf("containers used by %v (%v) after the retry, want %s", part.UsedBy, err, spec.Ref.Project)
	}
}

func TestTheContainerInfraAFailedDeployLeftUnusedIsTakenDownByItsRemoval(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	engine := &mockedEngine{outputs: containerPartOutputs()}
	stacks := stacksWith(cfg, engine)
	ctx := context.Background()
	tier := environment.TierProduction
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(the failing deploy's release) = %v", err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref, KeepParts: true}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	if err := stacks.containers.Remove(ctx, tier, progress.Discard()); err != nil {
		t.Fatalf("containers.Remove() of the infrastructure no project uses = %v", err)
	}
	if torn := engine.torn(); len(torn) != 2 || torn[1] != containers.StackRef(tier).Name.String() {
		t.Errorf("the removal tore down %v, want the container infrastructure stack after the failed release", torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, tier, containers.Part); err != nil || present {
		t.Errorf("containers record present %v (%v) after its removal, want it forgotten", present, err)
	}
}

func requireContainerInfraRecorded(t *testing.T, store records.Store) {
	t.Helper()
	ctx := context.Background()
	tier := environment.TierProduction
	if _, present, err := stackrecords.Read(ctx, store, tier, containers.Slug, containers.StackRef(tier).Name); err != nil || !present {
		t.Errorf("the shared container infrastructure is recorded %v (%v), want its stack record kept for the next deploy to find", present, err)
	}
	if recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, containers.Part); err != nil || !present || recorded.State != stackrecords.PartReady {
		t.Errorf("containers record = %+v, %v, %v, want it kept ready", recorded, present, err)
	}
	if _, recorded, err := awsports.ReadContainerFront(ctx, store, tier); err != nil || !recorded {
		t.Errorf("the front is recorded %v (%v), want it kept with the container infrastructure it names", recorded, err)
	}
}
