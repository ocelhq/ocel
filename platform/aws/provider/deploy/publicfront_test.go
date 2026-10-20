package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

const (
	fixturePublicListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-public-production/abc/def"
	fixturePublicHost     = "ocel-public-production-123.us-east-1.elb.amazonaws.com"
)

var fixtureRanges = []string{"198.51.100.0/24", "203.0.113.0/24"}

func TestTheFirstContainerRoutedByTheLoadBalancerRaisesThePublicFrontOnceAndHangsItsRuleThere(t *testing.T) {
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
	infra := containers.StackRef(environment.TierProduction).Name.String()
	runs := 0
	engine := &mockedEngine{outputs: outputs, upErr: func(stack string) error {
		if stack != infra {
			return nil
		}
		if runs++; runs == 2 {
			outputs["publicListenerArn"] = auto.OutputValue{Value: fixturePublicListener}
			outputs["publicHost"] = auto.OutputValue{Value: fixturePublicHost}
			outputs["publicRanges"] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
		}
		return nil
	}}
	stacks := stacksWith(cfg, engine)
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision(through the internal front) = %v", err)
	}

	behindCloudflare := spec
	app := *spec.App
	behindCloudflare.App = &app
	behindCloudflare.App.Router = alb.Kind
	behindCloudflare.Edge = rangedEdge{}
	for range 2 {
		if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
			t.Fatalf("Provision(behind the public front) = %v", err)
		}
	}
	if runs != 2 {
		t.Errorf("the container infrastructure ran %d times over %v, want twice: raised once, and raised again with the public front the first app behind it asked for", runs, engine.stacks())
	}
}

func TestThePublicFrontAdmitsTheEdgesCurrentRangesOnceTheyChange(t *testing.T) {
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
	infra := containers.StackRef(environment.TierProduction).Name.String()
	runs := 0
	engine := &mockedEngine{outputs: outputs, upErr: func(stack string) error {
		if stack != infra {
			return nil
		}
		runs++
		outputs["publicListenerArn"] = auto.OutputValue{Value: fixturePublicListener}
		outputs["publicHost"] = auto.OutputValue{Value: fixturePublicHost}
		outputs["publicRanges"] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
		return nil
	}}
	stacks := stacksWith(cfg, engine)
	behindCloudflare := spec
	app := *spec.App
	behindCloudflare.App = &app
	behindCloudflare.App.Router = alb.Kind
	behindCloudflare.Edge = rangedEdge{}
	ctx := context.Background()
	for range 2 {
		if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
			t.Fatalf("Provision(behind the public front) = %v", err)
		}
	}
	behindCloudflare.Edge = widenedEdge{}
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind an edge that forwards from a new range) = %v", err)
	}
	if runs != 2 {
		t.Errorf("the container infrastructure ran %d times, want twice: once to raise the public front, and once more when the edge forwards from a range the security group does not admit", runs)
	}
}

func TestTheContainersPartListsAndPricesThePublicFrontAForwardedContainerAppRaised(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	cfg.Namespace = "ocel"
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		container.OutputKeyURL:      "http://" + fixtureOrigin,
		container.OutputKeyPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	outputs["publicListenerArn"] = auto.OutputValue{Value: fixturePublicListener}
	outputs["publicHost"] = auto.OutputValue{Value: fixturePublicHost}
	outputs["publicRanges"] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
	outputs["livenessRoleArn"] = auto.OutputValue{Value: "arn:aws:iam::123456789012:role/ocel-containers-production-live-abc"}
	stacks := stacksWith(cfg, &mockedEngine{outputs: outputs})
	behindCloudflare := spec
	app := *spec.App
	behindCloudflare.App = &app
	behindCloudflare.App.Router = alb.Kind
	behindCloudflare.Edge = rangedEdge{}
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind the public front) = %v", err)
	}

	part, present, err := containers.ReadPart(ctx, cfg.KeyValues, "ocel", "us-east-1", environment.TierProduction)
	if err != nil || !present {
		t.Fatalf("containers.ReadPart() = %v, %v, want the containers part", present, err)
	}
	for _, want := range []provider.PartResource{
		{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: "ocel-prod-public"},
		{Kind: "AWS::Lambda::Function", ID: "ocel-prod-public-liveness"},
		{Kind: "AWS::IAM::Role", ID: "ocel-containers-production-live-abc"},
		{Kind: "AWS::IAM::Role", ID: "ocel-containers-production-exec-abc"},
	} {
		if !slices.Contains(part.Resources, want) {
			t.Errorf("resources = %v, want %v among them", part.Resources, want)
		}
	}
	var internal []bool
	for _, shaped := range part.Shape.Resources {
		if shaped.Type == "aws_lb" {
			internal = append(internal, shaped.Properties["internal"].(bool))
		}
	}
	if !slices.Contains(internal, false) {
		t.Errorf("shaped load balancers internal = %v, want the internet-facing one the edge forwards to", internal)
	}
}

type widenedEdge struct{ edge.Edge }

func (widenedEdge) Kind() edge.Kind { return "cloudflare" }

func (widenedEdge) Facts() edge.Facts {
	return edge.Facts{OriginFacingRanges: append(slices.Clone(fixtureRanges), "192.0.2.0/24")}
}

type rangedEdge struct{ edge.Edge }

func (rangedEdge) Kind() edge.Kind { return "cloudflare" }

func (rangedEdge) Facts() edge.Facts { return edge.Facts{OriginFacingRanges: fixtureRanges} }
