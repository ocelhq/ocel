package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
)

const (
	fixturePublicListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-public-production/abc/def"
	fixturePublicHost     = "ocel-public-production-123.us-east-1.elb.amazonaws.com"
)

var fixtureRanges = []string{"198.51.100.0/24", "203.0.113.0/24"}

func publicInfraWork() *containerInfraWork {
	return &containerInfraWork{
		tier:     environment.TierProduction,
		boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		tags:     containerInfraTags(environment.TierProduction),
		public:   fixtureRanges,
	}
}

func recordedNamed(t *testing.T, rec *inputRecorder, typeToken, suffix string) resource.PropertyMap {
	t.Helper()
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, typeToken+"::") && strings.HasSuffix(key, suffix) {
			return inputs
		}
	}
	t.Fatalf("the program declared no %s named *%s", typeToken, suffix)
	return nil
}

func TestThePublicFrontAnswersTLSOnlyFromTheRangesItsEdgeForwardsFrom(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	balancer := recordedNamed(t, rec, "aws:lb/loadBalancer:LoadBalancer", "public")
	if balancer["name"].StringValue() != "ocel-public-production" || balancer["internal"].BoolValue() {
		t.Errorf("public load balancer = %v, want an internet-facing one named for the tier", balancer)
	}
	listener := recordedNamed(t, rec, "aws:lb/listener:Listener", "public-listener")
	if listener["port"].NumberValue() != 443 || listener["protocol"].StringValue() != "HTTPS" || listener["certificateArn"].StringValue() == "" {
		t.Errorf("public listener = %v, want HTTPS on 443 with a default certificate", listener)
	}
	group := recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "public-security-group")
	ingress := group["ingress"].ArrayValue()
	tlsRule := slices.IndexFunc(ingress, func(rule resource.PropertyValue) bool { return rule.ObjectValue()["fromPort"].NumberValue() == 443 })
	if tlsRule < 0 {
		t.Fatalf("the public front admits %v, want a rule for 443", ingress)
	}
	rule := ingress[tlsRule].ObjectValue()
	var ranges []string
	for _, block := range rule["cidrBlocks"].ArrayValue() {
		ranges = append(ranges, block.StringValue())
	}
	if rule["fromPort"].NumberValue() != 443 || rule["toPort"].NumberValue() != 443 || !slices.Equal(ranges, fixtureRanges) {
		t.Errorf("the public front admits %v, want 443 from %v alone", rule, fixtureRanges)
	}
	tasks := recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "tasks-security-group")
	if sources := tasks["ingress"].ArrayValue()[0].ObjectValue()["securityGroups"].ArrayValue(); len(sources) != 2 {
		t.Errorf("the tasks admit %v, want both fronts: a release behind the public front is reached through it", sources)
	}
	liveness := recordedNamed(t, rec, "aws:lb/listenerRule:ListenerRule", "public-liveness-rule")
	condition := liveness["conditions"].ArrayValue()[0].ObjectValue()["pathPattern"].ObjectValue()
	if values := condition["values"].ArrayValue(); len(values) != 1 || values[0].StringValue() != edge.LivenessProbePath {
		t.Errorf("the liveness rule matches %v, want %s: a hostname's probe reads which router answers it there", condition, edge.LivenessProbePath)
	}
	function := recordedNamed(t, rec, "aws:lambda/function:Function", "public-liveness")
	if function["role"].StringValue() == "" {
		t.Errorf("the liveness function = %v, want a role", function)
	}
}

func TestThePublicFrontsLivenessAnswerRunsTheRuntimeAppFunctionsRun(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	function := recordedNamed(t, rec, "aws:lambda/function:Function", "public-liveness")
	if got := function["runtime"].StringValue(); got != defaultFunctionRuntime {
		t.Errorf("the liveness function runs %s, want %s, the runtime app functions run", got, defaultFunctionRuntime)
	}
}

func TestThePublicFrontRedirectsPlainHTTPToHTTPSForwardingNothing(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	listener := recordedNamed(t, rec, "aws:lb/listener:Listener", "public-redirect")
	action := listener["defaultActions"].ArrayValue()[0].ObjectValue()
	redirect := action["redirect"].ObjectValue()
	if listener["port"].NumberValue() != 80 || action["type"].StringValue() != "redirect" || redirect["protocol"].StringValue() != "HTTPS" || redirect["port"].StringValue() != "443" {
		t.Errorf("the plain-http listener = %v, want :80 answering every request with a redirect to https: Cloudflare reaches the origin over plain http when the visitor did", listener)
	}
	var ports []float64
	for _, rule := range recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "public-security-group")["ingress"].ArrayValue() {
		ports = append(ports, rule.ObjectValue()["fromPort"].NumberValue())
	}
	if !slices.Contains(ports, 80) || !slices.Contains(ports, 443) {
		t.Errorf("the public front admits ports %v, want 80 and 443 from the edge's ranges", ports)
	}
}

func TestTheContainerInfraRaisesNoPublicFrontForAnEdgeThatForwardsToNone(t *testing.T) {
	t.Parallel()

	work := publicInfraWork()
	work.public = nil
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}
	if balancers := rec.registered("aws:lb/loadBalancer:LoadBalancer"); len(balancers) != 1 {
		t.Errorf("declared load balancers %v, want the internal one alone: a public one costs by the hour whether anything answers behind it or not", balancers)
	}
}

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
	infra := containerInfraRef(environment.TierProduction).Name.String()
	runs := 0
	engine := &mockedEngine{outputs: outputs, upErr: func(stack string) error {
		if stack != infra {
			return nil
		}
		if runs++; runs == 2 {
			outputs[outputKeyPublicListener] = auto.OutputValue{Value: fixturePublicListener}
			outputs[outputKeyPublicHost] = auto.OutputValue{Value: fixturePublicHost}
			outputs[outputKeyPublicRanges] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
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
	infra := containerInfraRef(environment.TierProduction).Name.String()
	runs := 0
	engine := &mockedEngine{outputs: outputs, upErr: func(stack string) error {
		if stack != infra {
			return nil
		}
		runs++
		outputs[outputKeyPublicListener] = auto.OutputValue{Value: fixturePublicListener}
		outputs[outputKeyPublicHost] = auto.OutputValue{Value: fixturePublicHost}
		outputs[outputKeyPublicRanges] = auto.OutputValue{Value: strings.Join(fixtureRanges, ",")}
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
}

type rangedEdge struct{ edge.Edge }

func (rangedEdge) Kind() edge.Kind { return "cloudflare" }

func (rangedEdge) Facts() edge.Facts { return edge.Facts{OriginFacingRanges: fixtureRanges} }
