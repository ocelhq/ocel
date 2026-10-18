package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
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
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
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
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
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

type widenedEdge struct{ edge.Edge }

func (widenedEdge) Kind() edge.Kind { return "cloudflare" }

func (widenedEdge) Facts() edge.Facts {
	return edge.Facts{OriginFacingRanges: append(slices.Clone(fixtureRanges), "192.0.2.0/24")}
}

func TestAContainerRoutedByTheLoadBalancerIsReachedThroughThePublicFrontAlone(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec.App.Router = alb.Kind
	infra := fixtureContainerInfra()
	infra.PublicListener, infra.PublicHost = fixturePublicListener, fixturePublicHost
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, infra)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}

	rule := recordedOf(t, rec, "aws:lb/listenerRule:ListenerRule")
	if rule["listenerArn"].StringValue() != fixturePublicListener {
		t.Errorf("the release's rule hangs off %v, want the public listener: a target group answers behind one load balancer, and the router forwards this app's hostnames to it there", rule["listenerArn"])
	}
	env := definitionEnvOf(t, rec)
	if _, guarded := env[edge.OriginSecretVar]; guarded {
		t.Errorf("the container is handed %s, and a request the load balancer forwards by hostname carries no secret: it would refuse every one of them", edge.OriginSecretVar)
	}
	if _, fronted := env[edge.OriginFrontedVar]; fronted {
		t.Errorf("the container is handed %s, and no front presents a secret to it: it would refuse to start", edge.OriginFrontedVar)
	}
}

type rangedEdge struct{ edge.Edge }

func (rangedEdge) Kind() edge.Kind { return "cloudflare" }

func (rangedEdge) Facts() edge.Facts { return edge.Facts{OriginFacingRanges: fixtureRanges} }
