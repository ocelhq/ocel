package containers

import (
	"context"
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	fixturePublicListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-public-production/abc/def"
	fixturePublicHost     = "ocel-public-production-123.us-east-1.elb.amazonaws.com"
	fixtureLivenessRole   = "arn:aws:iam::123456789012:role/ocel/app/ocel-containers-production-live-abc"
	fixturePublicRedirect = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-public-production/abc/http"
	fixtureLivenessTarget = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/ocel-app-tg-abc123/def"
	fixtureLivenessRule   = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/ocel-public-production/abc/def/rule"
	fixtureRegion         = "us-east-1"
)

func fixturePublicOnlyInfrastructure() Infrastructure {
	infra := fixtureInfrastructure()
	infra.Listener, infra.OriginHost, infra.VPCOrigin = "", "", ""
	infra.PublicListener, infra.PublicHost, infra.PublicRanges = fixturePublicListener, fixturePublicHost, joinRanges(fixtureRanges)
	infra.LivenessRole = fixtureLivenessRole
	infra.PublicRedirectListener, infra.LivenessTargets, infra.LivenessRule = fixturePublicRedirect, fixtureLivenessTarget, fixtureLivenessRule
	return infra
}

func regional(kind, id string) provider.PhysicalResource {
	return provider.PhysicalResource{Kind: kind, ID: id, Location: fixtureRegion}
}

func global(kind, id string) provider.PhysicalResource {
	return provider.PhysicalResource{Kind: kind, ID: id, Location: "global"}
}

func internalFlagsOf(items []pricing.Item) []bool {
	var internal []bool
	for _, item := range items {
		if item.Type == component.TerraformLoadBalancer {
			internal = append(internal, item.Properties["internal"].(bool))
		}
	}
	return internal
}

func countItems(items []pricing.Item, typ string) int {
	count := 0
	for _, item := range items {
		if item.Type == typ {
			count++
		}
	}
	return count
}

func TestTheSharedInventoryListsTheLoadBalancerOfEachFrontItsContainerAppsAreRoutedThrough(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		fronts    Raised
		internal  []bool
		functions int
	}{
		{name: "through CloudFront", fronts: Raised{Private: true}, internal: []bool{true}},
		{name: "forwarded by an edge", fronts: Raised{Public: true}, internal: []bool{false}, functions: 1},
		{name: "both", fronts: Raised{Private: true, Public: true}, internal: []bool{true, false}, functions: 1},
		{name: "neither", fronts: Raised{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			items := Inventory(environment.TierProduction, tc.fronts)
			if got := internalFlagsOf(items); !slices.Equal(got, tc.internal) {
				t.Errorf("load balancers internal = %v, want %v", got, tc.internal)
			}
			if got := countItems(items, component.TerraformLambdaFunction); got != tc.functions {
				t.Errorf("functions = %d, want %d: the public front's liveness answer runs as one", got, tc.functions)
			}
			if countItems(items, component.TerraformECSCluster) != 1 || countItems(items, component.TerraformLogGroup) != 1 {
				t.Errorf("inventory = %+v, want the cluster and the log group every container app needs", items)
			}
			seen := map[string]bool{}
			for _, item := range items {
				if key := item.Type + ":" + item.Name; seen[key] {
					t.Errorf("inventory names %s twice, so one would stand in for the other", key)
				} else {
					seen[key] = true
				}
			}
		})
	}
}

func TestTheContainersPartListsThePublicFrontItRaisedAndNoPrivateFrontItDidNot(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	listed := listResources("ocel", fixtureRegion, tier, fixturePublicOnlyInfrastructure())

	for _, want := range []provider.PhysicalResource{
		regional("AWS::ECS::Cluster", "ocel-prod-containers"),
		regional("AWS::EC2::SecurityGroup", "ocel-prod-containers-tasks-sg"),
		regional("AWS::Logs::LogGroup", "/ecs/ocel-prod-containers"),
		global("AWS::IAM::Role", "ocel-containers-production-exec-abc"),
		regional("AWS::ElasticLoadBalancingV2::LoadBalancer", "ocel-prod-public"),
		regional("AWS::ElasticLoadBalancingV2::Listener", fixturePublicListener),
		regional("AWS::ElasticLoadBalancingV2::Listener", fixturePublicRedirect),
		regional("AWS::EC2::SecurityGroup", "ocel-prod-public-sg"),
		regional("AWS::CertificateManager::Certificate", "ocel-prod-public.invalid"),
		regional("AWS::Lambda::Function", "ocel-prod-public-liveness"),
		regional("AWS::ElasticLoadBalancingV2::TargetGroup", fixtureLivenessTarget),
		regional("AWS::ElasticLoadBalancingV2::ListenerRule", fixtureLivenessRule),
		global("AWS::IAM::Role", "ocel-containers-production-live-abc"),
	} {
		if !slices.Contains(listed, want) {
			t.Errorf("listed %v, want %v among them", listed, want)
		}
	}
	for _, unwanted := range []provider.PhysicalResource{
		regional("AWS::ElasticLoadBalancingV2::LoadBalancer", "ocel-prod-containers"),
		regional("AWS::EC2::SecurityGroup", "ocel-prod-containers-sg"),
	} {
		if slices.Contains(listed, unwanted) {
			t.Errorf("listed %v, the private front this raise never raised", unwanted)
		}
	}
	if slices.ContainsFunc(listed, func(r provider.PhysicalResource) bool { return r.Kind == "AWS::CloudFront::VpcOrigin" }) {
		t.Errorf("listed %v, want no VPC origin without the private front", listed)
	}
}

func TestTheContainersPartListsThePrivateFrontAndNoPublicFrontBehindCloudFront(t *testing.T) {
	t.Parallel()

	listed := listResources("ocel", fixtureRegion, environment.TierProduction, fixtureInfrastructure())

	for _, want := range []provider.PhysicalResource{
		regional("AWS::ElasticLoadBalancingV2::LoadBalancer", "ocel-prod-containers"),
		regional("AWS::ElasticLoadBalancingV2::Listener", fixtureListener),
		regional("AWS::EC2::SecurityGroup", "ocel-prod-containers-sg"),
		global("AWS::CloudFront::VpcOrigin", fixtureFront),
		global("AWS::IAM::Role", "ocel-containers-production-exec-abc"),
	} {
		if !slices.Contains(listed, want) {
			t.Errorf("listed %v, want %v among them", listed, want)
		}
	}
	if slices.Contains(listed, regional("AWS::ElasticLoadBalancingV2::LoadBalancer", "ocel-prod-public")) {
		t.Errorf("listed %v, want no public front nothing forwarded to", listed)
	}
}

func TestTheContainersPartDescribesWhatItsLastRaiseRecordedAndListsThoseFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	if err := stackrecords.Write(ctx, store, tier, Slug, StackRef(tier).Name, stackrecords.Stack{Kind: provider.StackInfra, WrittenBy: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	raised := listResources("ocel", fixtureRegion, tier, fixturePublicOnlyInfrastructure())
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, tier, Part, stackrecords.BootstrapPart{WrittenBy: "v1.0.0", AddedBy: "shop", Resources: raised}); err != nil {
		t.Fatal(err)
	}

	part, present, err := ReadPart(ctx, store, "ocel", "us-east-1", tier)
	if err != nil || !present {
		t.Fatalf("ReadPart() = %v, %v, want the containers part", present, err)
	}
	if !slices.Equal(part.Resources, raised) {
		t.Errorf("resources = %v, want the %v its raise recorded", part.Resources, raised)
	}
	if part.Inventory == nil {
		t.Fatal("the part has no inventory, so status cannot say what it costs")
	}
	if got := internalFlagsOf(part.Inventory.Resources); !slices.Equal(got, []bool{false}) {
		t.Errorf("listed load balancers internal = %v, want the internet-facing one alone it raised", got)
	}
}

func TestDecodeReadsTheRoleThePublicFrontsLivenessAnswerRunsAs(t *testing.T) {
	t.Parallel()

	outputs := infrastructureOutputs()
	outputs[outputKeyPublicListener] = auto.OutputValue{Value: fixturePublicListener}
	outputs[outputKeyLivenessRole] = auto.OutputValue{Value: fixtureLivenessRole}

	decoded, err := decode(outputs, true)
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if decoded.LivenessRole != fixtureLivenessRole {
		t.Errorf("liveness role = %q, want %q", decoded.LivenessRole, fixtureLivenessRole)
	}
}

func TestDecodeReadsThePublicFrontsRedirectListenerAndLivenessRouting(t *testing.T) {
	t.Parallel()

	outputs := infrastructureOutputs()
	outputs[outputKeyPublicListener] = auto.OutputValue{Value: fixturePublicListener}
	outputs[outputKeyPublicRedirectListener] = auto.OutputValue{Value: fixturePublicRedirect}
	outputs[outputKeyLivenessTargets] = auto.OutputValue{Value: fixtureLivenessTarget}
	outputs[outputKeyLivenessRule] = auto.OutputValue{Value: fixtureLivenessRule}

	decoded, err := decode(outputs, true)
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if decoded.PublicRedirectListener != fixturePublicRedirect || decoded.LivenessTargets != fixtureLivenessTarget || decoded.LivenessRule != fixtureLivenessRule {
		t.Errorf("redirect listener, liveness targets, rule = %q, %q, %q, want %q, %q, %q",
			decoded.PublicRedirectListener, decoded.LivenessTargets, decoded.LivenessRule,
			fixturePublicRedirect, fixtureLivenessTarget, fixtureLivenessRule)
	}
}
