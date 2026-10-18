package deploy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

func containerInfraOutputs() auto.OutputMap {
	infra := fixtureContainerInfra()
	subnets, _ := json.Marshal(infra.Subnets)
	return auto.OutputMap{
		"vpcId":             auto.OutputValue{Value: infra.VPC},
		"subnets":           auto.OutputValue{Value: string(subnets)},
		"listenerArn":       auto.OutputValue{Value: infra.Listener},
		"originHost":        auto.OutputValue{Value: infra.OriginHost},
		"vpcOrigin":         auto.OutputValue{Value: infra.VPCOrigin},
		"taskSecurityGroup": auto.OutputValue{Value: infra.TaskSecurity},
		"cluster":           auto.OutputValue{Value: infra.Cluster},
		"executionRoleArn":  auto.OutputValue{Value: infra.ExecutionRole},
		"logGroup":          auto.OutputValue{Value: infra.LogGroup},
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
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
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

func TestAContainerDeployThatFailsLeavesNoConsumerBehind(t *testing.T) {
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
	if torn := engine.torn(); len(torn) != 2 || torn[0] != spec.Ref.Name.String() || torn[1] != containers.StackRef(environment.TierProduction).Name.String() {
		t.Errorf("after the only consumer failed the engine tore down %v, want the half-built app stack first and then the container infrastructure stack it had just provisioned: a cluster with a service inside refuses to go, and nothing idle-billing outlives a failed first deploy", torn)
	}

	spec.App.HealthCheckPath = "not a path"
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err == nil {
		t.Fatal("Provision accepted a health check path a load balancer cannot probe")
	}
	if ran := engine.stacks(); len(ran) != 2 {
		t.Errorf("a refused deploy ran %v, want no second container infrastructure stack: the app is checked before anything is provisioned", ran)
	}
}
