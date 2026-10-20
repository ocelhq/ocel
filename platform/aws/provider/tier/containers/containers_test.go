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
)

func fixturePublicOnlyInfrastructure() Infrastructure {
	infra := fixtureInfrastructure()
	infra.Listener, infra.OriginHost, infra.VPCOrigin = "", "", ""
	infra.PublicListener, infra.PublicHost, infra.PublicRanges = fixturePublicListener, fixturePublicHost, joinRanges(fixtureRanges)
	infra.LivenessRole = fixtureLivenessRole
	return infra
}

func internalFlagsOf(shaped []pricing.Shaped) []bool {
	var internal []bool
	for _, item := range shaped {
		if item.Type == component.TerraformLoadBalancer {
			internal = append(internal, item.Properties["internal"].(bool))
		}
	}
	return internal
}

func countShaped(shaped []pricing.Shaped, typ string) int {
	count := 0
	for _, item := range shaped {
		if item.Type == typ {
			count++
		}
	}
	return count
}

func TestTheSharedShapePricesTheLoadBalancerOfEachFrontItsContainerAppsAreRoutedThrough(t *testing.T) {
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

			shaped := Shape(environment.TierProduction, tc.fronts)
			if got := internalFlagsOf(shaped); !slices.Equal(got, tc.internal) {
				t.Errorf("load balancers internal = %v, want %v", got, tc.internal)
			}
			if got := countShaped(shaped, component.TerraformLambdaFunction); got != tc.functions {
				t.Errorf("functions = %d, want %d: the public front's liveness answer runs as one", got, tc.functions)
			}
			if countShaped(shaped, component.TerraformECSCluster) != 1 || countShaped(shaped, component.TerraformLogGroup) != 1 {
				t.Errorf("shape = %+v, want the cluster and the log group every container app needs", shaped)
			}
			seen := map[string]bool{}
			for _, item := range shaped {
				if key := item.Type + ":" + item.Name; seen[key] {
					t.Errorf("shape names %s twice, so one would price over the other", key)
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
	listed := listResources("ocel", tier, fixturePublicOnlyInfrastructure())

	for _, want := range []provider.PartResource{
		{Kind: "AWS::ECS::Cluster", ID: "ocel-prod-containers"},
		{Kind: "AWS::EC2::SecurityGroup", ID: "ocel-prod-containers-tasks-sg"},
		{Kind: "AWS::Logs::LogGroup", ID: "/ecs/ocel-prod-containers"},
		{Kind: "AWS::IAM::Role", ID: "ocel-containers-production-exec-abc"},
		{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: "ocel-prod-public"},
		{Kind: "AWS::EC2::SecurityGroup", ID: "ocel-prod-public-sg"},
		{Kind: "AWS::CertificateManager::Certificate", ID: "ocel-prod-public.invalid"},
		{Kind: "AWS::Lambda::Function", ID: "ocel-prod-public-liveness"},
		{Kind: "AWS::IAM::Role", ID: "ocel-containers-production-live-abc"},
	} {
		if !slices.Contains(listed, want) {
			t.Errorf("listed %v, want %v among them", listed, want)
		}
	}
	for _, unwanted := range []provider.PartResource{
		{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: "ocel-prod-containers"},
		{Kind: "AWS::EC2::SecurityGroup", ID: "ocel-prod-containers-sg"},
	} {
		if slices.Contains(listed, unwanted) {
			t.Errorf("listed %v, the private front this raise never raised", unwanted)
		}
	}
	if slices.ContainsFunc(listed, func(r provider.PartResource) bool { return r.Kind == "AWS::CloudFront::VpcOrigin" }) {
		t.Errorf("listed %v, want no VPC origin without the private front", listed)
	}
}

func TestTheContainersPartListsThePrivateFrontAndNoPublicFrontBehindCloudFront(t *testing.T) {
	t.Parallel()

	listed := listResources("ocel", environment.TierProduction, fixtureInfrastructure())

	for _, want := range []provider.PartResource{
		{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: "ocel-prod-containers"},
		{Kind: "AWS::EC2::SecurityGroup", ID: "ocel-prod-containers-sg"},
		{Kind: "AWS::CloudFront::VpcOrigin", ID: fixtureFront},
		{Kind: "AWS::IAM::Role", ID: "ocel-containers-production-exec-abc"},
	} {
		if !slices.Contains(listed, want) {
			t.Errorf("listed %v, want %v among them", listed, want)
		}
	}
	if slices.Contains(listed, provider.PartResource{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: "ocel-prod-public"}) {
		t.Errorf("listed %v, want no public front nothing forwarded to", listed)
	}
}

func TestTheContainersPartDescribesWhatItsLastRaiseRecordedAndPricesThoseFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	if err := stackrecords.Write(ctx, store, tier, Slug, StackRef(tier).Name, stackrecords.Stack{Kind: provider.StackInfra, WrittenBy: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	raised := listResources("ocel", tier, fixturePublicOnlyInfrastructure())
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
	if part.Shape == nil {
		t.Fatal("the part has no shape, so status cannot say what it costs")
	}
	if got := internalFlagsOf(part.Shape.Resources); !slices.Equal(got, []bool{false}) {
		t.Errorf("shaped load balancers internal = %v, want the internet-facing one alone it raised", got)
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
