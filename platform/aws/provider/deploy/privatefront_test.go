package deploy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

const vpcOriginToken = "aws:cloudfront/vpcOrigin:VpcOrigin"

type declaredPerRun struct {
	standInCloud
	mu       sync.Mutex
	declared []string
}

func (d *declaredPerRun) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	d.mu.Lock()
	d.declared = append(d.declared, args.TypeToken+"::"+args.Name)
	d.mu.Unlock()
	return d.standInCloud.NewResource(args)
}

func (d *declaredPerRun) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	taken := d.declared
	d.declared = nil
	return taken
}

func declaresKind(declared []string, typeToken string) bool {
	return slices.ContainsFunc(declared, func(key string) bool { return strings.HasPrefix(key, typeToken+"::") })
}

func declaresPublicFront(declared []string) bool {
	return slices.Contains(declared, "aws:lb/loadBalancer:LoadBalancer::svc-public")
}

func publicOnlyInfraOutputs() auto.OutputMap {
	outputs := containerInfraOutputs()
	delete(outputs, "listenerArn")
	delete(outputs, "originHost")
	delete(outputs, "vpcOrigin")
	addPublicFront(outputs)
	return outputs
}

func addPublicFront(outputs auto.OutputMap) {
	outputs["publicListenerArn"] = auto.OutputValue{Value: fixturePublicListener}
	outputs["publicHost"] = auto.OutputValue{Value: fixturePublicHost}
	outputs["publicRanges"] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
}

func addPrivateFront(outputs auto.OutputMap) {
	infra := fixtureContainerInfra()
	outputs["listenerArn"] = auto.OutputValue{Value: infra.Listener}
	outputs["originHost"] = auto.OutputValue{Value: infra.OriginHost}
	outputs["vpcOrigin"] = auto.OutputValue{Value: infra.VPCOrigin}
}

type infraRuns struct {
	mu   sync.Mutex
	runs [][]string
}

func (r *infraRuns) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func (r *infraRuns) at(i int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[i]
}

type frontsCase struct {
	stacks            *Stacks
	runs              *infraRuns
	store             records.Store
	throughCloudFront provider.StackSpec
	behindCloudflare  provider.StackSpec
}

func recordingInfraRuns(t *testing.T, outputs auto.OutputMap, afterRun func(run int)) frontsCase {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		container.OutputKeyURL:      "http://" + fixtureOrigin,
		container.OutputKeyPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	infra := containers.StackRef(environment.TierProduction).Name.String()
	declared := &declaredPerRun{}
	runs := &infraRuns{}
	engine := &mockedEngine{outputs: outputs, mocks: declared, upErr: func(stack string) error {
		taken := declared.take()
		if stack != infra {
			return nil
		}
		runs.mu.Lock()
		runs.runs = append(runs.runs, taken)
		run := len(runs.runs)
		runs.mu.Unlock()
		if afterRun != nil {
			afterRun(run)
		}
		return nil
	}}
	stacks := stacksWith(cfg, engine)
	behindCloudflare := spec
	app := *spec.App
	behindCloudflare.App = &app
	behindCloudflare.App.Router = alb.Kind
	behindCloudflare.Edge = rangedEdge{}
	return frontsCase{stacks: stacks, runs: runs, store: cfg.KeyValues, throughCloudFront: spec, behindCloudflare: behindCloudflare}
}

func TestAContainerBehindAnEdgeThatForwardsPubliclyRaisesNoPrivateFrontAndRecordsNoContainerFront(t *testing.T) {
	t.Parallel()

	c := recordingInfraRuns(t, publicOnlyInfraOutputs(), nil)
	stacks, runs, behindCloudflare := c.stacks, c.runs, c.behindCloudflare
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind the public front) = %v", err)
	}

	if runs.count() != 1 {
		t.Fatalf("the container infrastructure ran %d times, want once", runs.count())
	}
	declared := runs.at(0)
	if declaresKind(declared, vpcOriginToken) || slices.Contains(declared, "aws:lb/loadBalancer:LoadBalancer::svc-front") {
		t.Errorf("the container infrastructure declared %v, want no internal load balancer and no VPC origin: no app in the tier is routed through CloudFront", declared)
	}
	if !declaresPublicFront(declared) {
		t.Errorf("the container infrastructure declared %v, want the public front the app is routed through", declared)
	}
	if _, recorded, err := awsports.ReadContainerFront(ctx, c.store, environment.TierProduction); err != nil || recorded {
		t.Errorf("the container front is recorded %v (%v), want none: there is no VPC origin for an edge to reach", recorded, err)
	}
}

func TestAContainerRoutedThroughCloudFrontRaisesThePrivateFrontAndKeepsThePublicOneAnotherAppUses(t *testing.T) {
	t.Parallel()

	outputs := publicOnlyInfraOutputs()
	c := recordingInfraRuns(t, outputs, func(run int) {
		if run == 2 {
			addPrivateFront(outputs)
		}
	})
	stacks, runs, throughCloudFront, behindCloudflare := c.stacks, c.runs, c.throughCloudFront, c.behindCloudflare
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind the public front) = %v", err)
	}
	for range 2 {
		if _, err := stacks.Provision(ctx, throughCloudFront, progress.Discard()); err != nil {
			t.Fatalf("Provision(through CloudFront) = %v", err)
		}
	}

	if runs.count() != 2 {
		t.Fatalf("the container infrastructure ran %d times, want twice: raised with the public front, and raised again with the private front the first app routed through CloudFront asked for", runs.count())
	}
	declared := runs.at(1)
	if !declaresKind(declared, vpcOriginToken) {
		t.Errorf("the second run declared %v, want the VPC origin CloudFront reaches the app through", declared)
	}
	if !declaresPublicFront(declared) {
		t.Errorf("the second run declared %v, want the public front kept: another app still answers behind it", declared)
	}
	front, recorded, err := awsports.ReadContainerFront(ctx, c.store, environment.TierProduction)
	if err != nil || !recorded || front != (awsports.ContainerFront{VPCOrigin: fixtureFront, Host: fixtureOrigin}) {
		t.Errorf("the container front = %+v, recorded %v (%v), want the private front's VPC origin and host for the edge to reach", front, recorded, err)
	}
}

func TestRaisingThePublicFrontKeepsThePrivateFrontAnotherAppUses(t *testing.T) {
	t.Parallel()

	outputs := containerInfraOutputs()
	c := recordingInfraRuns(t, outputs, func(run int) {
		if run == 2 {
			addPublicFront(outputs)
		}
	})
	stacks, runs, throughCloudFront, behindCloudflare := c.stacks, c.runs, c.throughCloudFront, c.behindCloudflare
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, throughCloudFront, progress.Discard()); err != nil {
		t.Fatalf("Provision(through CloudFront) = %v", err)
	}
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind the public front) = %v", err)
	}

	if runs.count() != 2 {
		t.Fatalf("the container infrastructure ran %d times, want twice", runs.count())
	}
	if declared := runs.at(1); !declaresKind(declared, vpcOriginToken) || !declaresPublicFront(declared) {
		t.Errorf("the second run declared %v, want both fronts: the app routed through CloudFront still answers behind the private one", declared)
	}
}

func TestAPlanForAContainerRoutedThroughCloudFrontUpdatesATierThatHasOnlyThePublicFront(t *testing.T) {
	t.Parallel()

	c := recordingInfraRuns(t, publicOnlyInfraOutputs(), nil)
	stacks, throughCloudFront, behindCloudflare := c.stacks, c.throughCloudFront, c.behindCloudflare
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, behindCloudflare, progress.Discard()); err != nil {
		t.Fatalf("Provision(behind the public front) = %v", err)
	}

	plan, err := stacks.Plan(ctx, throughCloudFront, progress.Discard())
	if err != nil {
		t.Fatalf("Plan(through CloudFront) = %v", err)
	}
	if !planGroupFor(plan.Groups, containers.StackRef(environment.TierProduction).Name.String(), provider.ActionUpdate) {
		t.Errorf("plan groups = %v, want the shared container infrastructure updated to raise the private front", plan.Groups)
	}
}
