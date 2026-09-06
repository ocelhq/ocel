package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func substrateOutputs() auto.OutputMap {
	held := fixtureSubstrate()
	subnets, _ := json.Marshal(held.Subnets)
	return auto.OutputMap{
		outputKeyVPC:           auto.OutputValue{Value: held.VPC},
		outputKeySubnets:       auto.OutputValue{Value: string(subnets)},
		outputKeyListener:      auto.OutputValue{Value: held.Listener},
		outputKeyOriginHost:    auto.OutputValue{Value: held.OriginHost},
		outputKeyTaskSecurity:  auto.OutputValue{Value: held.TaskSecurity},
		outputKeyCluster:       auto.OutputValue{Value: held.Cluster},
		outputKeyExecutionRole: auto.OutputValue{Value: held.ExecutionRole},
		outputKeyLogGroup:      auto.OutputValue{Value: held.LogGroup},
	}
}

func TestTheSubstrateProgramStandsUpOneFrontOneClusterAndOneExecutionRole(t *testing.T) {
	t.Parallel()

	work := &substrateWork{class: providerkit.ClassProduction, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: substrateTags(providerkit.ClassProduction)}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the substrate program: %v", err)
	}

	listener := recordedOf(t, rec, "aws:lb/listener:Listener")
	action := listener["defaultActions"].ArrayValue()[0].ObjectValue()
	if action["type"].StringValue() != "fixed-response" || action["fixedResponse"].ObjectValue()["statusCode"].StringValue() != substrateDeniedStatus {
		t.Errorf("default action = %v, want a fixed %s: a request that names no release reaches nothing", action, substrateDeniedStatus)
	}
	balancer := recordedOf(t, rec, "aws:lb/loadBalancer:LoadBalancer")
	if balancer["name"].StringValue() != "ocel-containers-production" || balancer["internal"].BoolValue() {
		t.Errorf("load balancer = %v, want an internet-facing front named for the class", balancer)
	}
	role := recordedOf(t, rec, "aws:iam/role:Role")
	if !strings.Contains(role["assumeRolePolicy"].StringValue(), ecsTasksPrincipal) {
		t.Errorf("the execution role trusts %q, want %s", role["assumeRolePolicy"].StringValue(), ecsTasksPrincipal)
	}
	if len(rec.attached) != 1 || rec.attached[0] != ecsTaskExecutionPolicyARN {
		t.Errorf("attached %v, want only the ECS task execution policy", rec.attached)
	}
	groups := 0
	for key, inputs := range rec.recorded {
		if !strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") {
			continue
		}
		groups++
		ingress := inputs["ingress"].ArrayValue()[0].ObjectValue()
		if strings.HasSuffix(key, "tasks-security-group") && ingress["fromPort"].NumberValue() != containerPortNumber {
			t.Errorf("the task security group admits port %v, want %d and only from the front", ingress["fromPort"], containerPortNumber)
		}
		if strings.HasSuffix(key, "front-security-group") {
			if _, open := ingress["cidrBlocks"]; open || len(ingress["prefixListIds"].ArrayValue()) != 1 {
				t.Errorf("the front admits %v, want only CloudFront's origin-facing prefix list: the secret must never cross the open internet", ingress)
			}
		}
	}
	if groups != 2 {
		t.Errorf("declared %d security groups, want one for the front and one for the tasks", groups)
	}
}

func TestDecodeSubstrateReadsEveryOutputAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	decoded, err := decodeSubstrate(substrateOutputs())
	if err != nil {
		t.Fatalf("decodeSubstrate() = %v", err)
	}
	if decoded.OriginHost != fixtureOrigin || len(decoded.Subnets) != 2 || decoded.Listener != fixtureListener {
		t.Errorf("decoded = %+v, want every output the program exported", decoded)
	}
	partial := substrateOutputs()
	delete(partial, outputKeyListener)
	if _, err := decodeSubstrate(partial); err == nil {
		t.Error("decodeSubstrate accepted outputs with no listener, so a container would have no rule to hang off")
	}
}

func TestTheFirstContainerDeployStandsUpTheSubstrateAndTheLastTakesItDown(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	cfg.Records = fake.NewRecords()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs := substrateOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	engine := &mockedEngine{outputs: outputs}
	releaser := standingUp(cfg, engine)
	ctx := context.Background()

	shop := plan
	if _, err := releaser.Provision(ctx, shop, edge.DiscardReporter()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	ran := engine.stacks()
	if len(ran) != 2 || ran[0] != substrateRef(providerkit.ClassProduction).Name.String() || ran[1] != shop.Ref.Name.String() {
		t.Fatalf("the first container deploy ran %v, want the substrate stack before the app stack", ran)
	}

	blog := plan
	blog.Ref = providerkit.StackRef{Project: "blog", Class: providerkit.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	if _, err := releaser.Provision(ctx, blog, edge.DiscardReporter()); err != nil {
		t.Fatalf("Provision(blog) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 3 {
		t.Fatalf("the second container deploy ran %v, want the substrate reused rather than stood up again", ran)
	}

	if err := releaser.Destroy(ctx, shop.Ref, edge.DiscardReporter()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if destroyed := engine.torn(); len(destroyed) != 1 {
		t.Fatalf("destroying one of two container stacks tore down %v, want only its own: the other still answers behind the front", destroyed)
	}
	if err := releaser.Destroy(ctx, blog.Ref, edge.DiscardReporter()); err != nil {
		t.Fatalf("Destroy(blog) = %v", err)
	}
	destroyed := engine.torn()
	if len(destroyed) != 3 || destroyed[2] != substrateRef(providerkit.ClassProduction).Name.String() {
		t.Fatalf("destroying the last container stack tore down %v, want the substrate to go with it: nothing idle-billing survives the last container", destroyed)
	}
	if _, present, err := providerkit.ReadStack(ctx, cfg.Records, providerkit.ClassProduction, SubstrateSlug, substrateRef(providerkit.ClassProduction).Name); err != nil || present {
		t.Errorf("the substrate is still recorded (present %v, err %v) after its last consumer left", present, err)
	}
}

func TestAContainerDeployThatFailsLeavesNoConsumerBehind(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	cfg.Records = fake.NewRecords()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	engine := &mockedEngine{outputs: substrateOutputs()}
	releaser := standingUp(cfg, engine)
	ctx := context.Background()

	if _, err := releaser.Provision(ctx, plan, edge.DiscardReporter()); err == nil {
		t.Fatal("Provision succeeded with no container output, so a deploy would record a container with no origin")
	}
	remaining, err := cfg.Records.List(ctx, consumersRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("a failed deploy left %d consumer records, so the substrate could never be taken down", len(remaining))
	}
	if torn := engine.torn(); len(torn) != 2 || torn[0] != plan.Ref.Name.String() || torn[1] != substrateRef(providerkit.ClassProduction).Name.String() {
		t.Errorf("after the only consumer failed the engine tore down %v, want the half-built app stack first and then the substrate it had just stood up: a cluster with a service inside refuses to go, and nothing idle-billing outlives a failed first deploy", torn)
	}

	plan.App.HealthCheckPath = "not a path"
	if _, err := releaser.Provision(ctx, plan, edge.DiscardReporter()); err == nil {
		t.Fatal("Provision accepted a health check path a load balancer cannot probe")
	}
	if ran := engine.stacks(); len(ran) != 2 {
		t.Errorf("a refused deploy ran %v, want no second substrate stand-up: the app is checked before anything is stood up", ran)
	}
}
