package containers

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func TestTheContainerInfraProgramProvisionsOneFrontOneClusterAndOneExecutionRole(t *testing.T) {
	t.Parallel()

	infra := &work{network: testNetwork, namespace: "ocel", tier: environment.TierProduction, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags("ocel", environment.TierProduction), fronts: Fronts{Private: true}}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	listener := recordedOf(t, rec, "aws:lb/listener:Listener")
	action := listener["defaultActions"].ArrayValue()[0].ObjectValue()
	if action["type"].StringValue() != "fixed-response" || action["fixedResponse"].ObjectValue()["statusCode"].StringValue() != listenerDeniedStatus {
		t.Errorf("default action = %v, want a fixed %s: a request that names no release reaches nothing", action, listenerDeniedStatus)
	}
	balancer := recordedOf(t, rec, "aws:lb/loadBalancer:LoadBalancer")
	if balancer["name"].StringValue() != "ocel-prod-containers" || !balancer["internal"].BoolValue() {
		t.Errorf("load balancer = %v, want an internal front named for the tier: nothing on the internet has a route to it", balancer)
	}
	vpcOrigin := recordedOf(t, rec, "aws:cloudfront/vpcOrigin:VpcOrigin")
	endpoint := vpcOrigin["vpcOriginEndpointConfig"].ObjectValue()
	if endpoint["arn"].StringValue() == "" || !strings.HasPrefix(endpoint["name"].StringValue(), "ocel-prod-containers-") {
		t.Errorf("vpc origin = %v, want it to name the tier front by its ARN: the edge reaches the front through it and nothing else", endpoint)
	}
	if endpoint["originProtocolPolicy"].StringValue() != vpcOriginProtocolPolicy || endpoint["httpPort"].NumberValue() != originListenerPort {
		t.Errorf("vpc origin = %v, want CloudFront to reach the listener on %d over its private path", endpoint, originListenerPort)
	}
	role := recordedOf(t, rec, "aws:iam/role:Role")
	if !strings.Contains(role["assumeRolePolicy"].StringValue(), iam.ECSTasksPrincipal) {
		t.Errorf("the execution role trusts %q, want %s", role["assumeRolePolicy"].StringValue(), iam.ECSTasksPrincipal)
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
		if strings.HasSuffix(key, "::ocel-prod-containers-tasks-sg") && ingress["fromPort"].NumberValue() != TaskPort {
			t.Errorf("the task security group admits port %v, want %d and only from the front", ingress["fromPort"], TaskPort)
		}
		if strings.HasSuffix(key, "::ocel-prod-containers-sg") {
			if _, open := ingress["cidrBlocks"]; open || len(ingress["prefixListIds"].ArrayValue()) != 1 {
				t.Errorf("the front admits %v, want only CloudFront's origin-facing prefix list: the secret must never cross the open internet", ingress)
			}
		}
	}
	if groups != 2 {
		t.Errorf("declared %d security groups, want one for the front and one for the tasks", groups)
	}
}

func TestTheContainerSecurityGroupsKeepTheDescriptionsTheyWereCreatedWith(t *testing.T) {
	t.Parallel()

	infra := &work{network: testNetwork, namespace: "ocel", tier: environment.TierPreview, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags("ocel", environment.TierPreview), fronts: Fronts{Private: true}}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "preview--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	want := map[string]string{
		"::ocel-preview-containers-sg":       "Ocel: the load balancer every container app in the preview class answers behind",
		"::ocel-preview-containers-tasks-sg": "Ocel: the tasks every container app in the preview class runs as",
	}
	for key, inputs := range rec.recorded {
		if !strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") {
			continue
		}
		for suffix, description := range want {
			if strings.HasSuffix(key, suffix) && inputs["description"].StringValue() != description {
				t.Errorf("%s is described %q, want %q: a new description replaces the group, and the replacement cannot take the name the group already holds", suffix, inputs["description"].StringValue(), description)
			}
		}
	}
}

func TestDecodeContainerInfraReadsEveryOutputAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	decoded, err := decode(infrastructureOutputs(), true)
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if decoded.OriginHost != fixtureOrigin || decoded.VPCOrigin != fixtureFront || len(decoded.Subnets) != 2 || decoded.Listener != fixtureListener {
		t.Errorf("decoded = %+v, want every output the program exported", decoded)
	}
	partial := infrastructureOutputs()
	delete(partial, outputKeyListener)
	if _, err := decode(partial, true); err == nil {
		t.Error("decode accepted outputs with no listener, so a container would have no rule to hang off")
	}
}

type vpcOriginRegistrations struct {
	inputRecorder
	registerMu sync.Mutex
	names      []string
	ignored    []string
}

func (v *vpcOriginRegistrations) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "aws:cloudfront/vpcOrigin:VpcOrigin" {
		v.registerMu.Lock()
		v.names = append(v.names, args.Inputs["vpcOriginEndpointConfig"].ObjectValue()["name"].StringValue())
		if args.RegisterRPC != nil {
			v.ignored = append(v.ignored, args.RegisterRPC.GetIgnoreChanges()...)
		}
		v.registerMu.Unlock()
	}
	return v.inputRecorder.NewResource(args)
}

func TestTheFrontsVPCOriginTakesAFreshNameEachTimeItIsCreatedAndKeepsTheOneItWasCreatedWith(t *testing.T) {
	t.Parallel()

	registrations := &vpcOriginRegistrations{}
	for range 2 {
		if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", registrations)); err != nil {
			t.Fatalf("run the container infrastructure program: %v", err)
		}
	}

	registrations.registerMu.Lock()
	defer registrations.registerMu.Unlock()
	if len(registrations.names) != 2 || registrations.names[0] == registrations.names[1] {
		t.Errorf("the VPC origin was named %v across two runs, want a fresh name each time: CloudFront holds a deleted origin's name for a while after it is gone, so a retry under the same name is refused", registrations.names)
	}
	for _, name := range registrations.names {
		tag, under := strings.CutPrefix(name, nameFor("ocel", environment.TierProduction)+"-")
		if !under || !naming.IsRandomTag(tag) {
			t.Errorf("the VPC origin is named %q, want the tier's name and a random tag", name)
		}
	}
	if !slices.Contains(registrations.ignored, "vpcOriginEndpointConfig.name") {
		t.Errorf("the VPC origin ignores changes to %v, want its name among them: an origin keeps the name it was created with", registrations.ignored)
	}
}

func TestTheContainerInfraRunsInThePublicSubnetsOfTheTiersNetwork(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	for key, balancer := range rec.recorded {
		if !strings.HasPrefix(key, "aws:lb/loadBalancer:LoadBalancer::") {
			continue
		}
		var subnets []string
		for _, subnet := range balancer["subnets"].ArrayValue() {
			subnets = append(subnets, subnet.StringValue())
		}
		if !slices.Equal(subnets, testNetwork.Public) {
			t.Errorf("%s sits in %v, want the network's public subnets %v, which sit only in zones a VPC origin reaches", key, subnets, testNetwork.Public)
		}
	}
	for key, group := range rec.recorded {
		if strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") && group["vpcId"].StringValue() != testNetwork.VPC {
			t.Errorf("%s sits in %q, want the tier's network %s", key, group["vpcId"].StringValue(), testNetwork.VPC)
		}
	}
}

func TestTheContainerInfraIsRefusedWithoutATierNetworkToRunIn(t *testing.T) {
	t.Parallel()

	infra := publicInfraWork()
	infra.network = network.Network{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", &inputRecorder{})); err == nil {
		t.Fatal("run = nil, want the container infrastructure refused with no network to run in")
	}
}
