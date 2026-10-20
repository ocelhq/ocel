package containers

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func publicInfraWork() *work {
	return &work{
		network:   testNetwork,
		namespace: "ocel",
		tier:      environment.TierProduction,
		boundary:  "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		tags:      stackTags("ocel", environment.TierProduction),
		fronts:    Fronts{Private: true, Public: fixtureRanges},
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

	balancer := recordedNamed(t, rec, "aws:lb/loadBalancer:LoadBalancer", "::ocel-prod-public")
	if balancer["name"].StringValue() != "ocel-prod-public" || balancer["internal"].BoolValue() {
		t.Errorf("public load balancer = %v, want an internet-facing one named for the tier", balancer)
	}
	listener := recordedNamed(t, rec, "aws:lb/listener:Listener", "::ocel-prod-public/https")
	if listener["port"].NumberValue() != 443 || listener["protocol"].StringValue() != "HTTPS" || listener["certificateArn"].StringValue() == "" {
		t.Errorf("public listener = %v, want HTTPS on 443 with a default certificate", listener)
	}
	group := recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "::ocel-prod-public-sg")
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
	tasks := recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "::ocel-prod-containers-tasks-sg")
	if sources := tasks["ingress"].ArrayValue()[0].ObjectValue()["securityGroups"].ArrayValue(); len(sources) != 2 {
		t.Errorf("the tasks admit %v, want both fronts: a release behind the public front is reached through it", sources)
	}
	liveness := recordedNamed(t, rec, "aws:lb/listenerRule:ListenerRule", "::ocel-prod-public/https/liveness")
	condition := liveness["conditions"].ArrayValue()[0].ObjectValue()["pathPattern"].ObjectValue()
	if values := condition["values"].ArrayValue(); len(values) != 1 || values[0].StringValue() != edge.LivenessProbePath {
		t.Errorf("the liveness rule matches %v, want %s: a hostname's probe reads which router answers it there", condition, edge.LivenessProbePath)
	}
	function := recordedNamed(t, rec, "aws:lambda/function:Function", "::ocel-prod-public-liveness")
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

	function := recordedNamed(t, rec, "aws:lambda/function:Function", "::ocel-prod-public-liveness")
	if got := function["runtime"].StringValue(); got != component.LambdaNodeRuntime {
		t.Errorf("the liveness function runs %s, want %s, the runtime app functions run", got, component.LambdaNodeRuntime)
	}
}

func TestThePublicFrontRedirectsPlainHTTPToHTTPSForwardingNothing(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	listener := recordedNamed(t, rec, "aws:lb/listener:Listener", "::ocel-prod-public/http")
	action := listener["defaultActions"].ArrayValue()[0].ObjectValue()
	redirect := action["redirect"].ObjectValue()
	if listener["port"].NumberValue() != 80 || action["type"].StringValue() != "redirect" || redirect["protocol"].StringValue() != "HTTPS" || redirect["port"].StringValue() != "443" {
		t.Errorf("the plain-http listener = %v, want :80 answering every request with a redirect to https: Cloudflare reaches the origin over plain http when the visitor did", listener)
	}
	var ports []float64
	for _, rule := range recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "::ocel-prod-public-sg")["ingress"].ArrayValue() {
		ports = append(ports, rule.ObjectValue()["fromPort"].NumberValue())
	}
	if !slices.Contains(ports, 80) || !slices.Contains(ports, 443) {
		t.Errorf("the public front admits ports %v, want 80 and 443 from the edge's ranges", ports)
	}
}

func TestTheContainerInfraRaisesNoPublicFrontForAnEdgeThatForwardsToNone(t *testing.T) {
	t.Parallel()

	infra := publicInfraWork()
	infra.fronts.Public = nil
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}
	if balancers := rec.registered("aws:lb/loadBalancer:LoadBalancer"); len(balancers) != 1 {
		t.Errorf("declared load balancers %v, want the internal one alone: a public one costs by the hour whether anything answers behind it or not", balancers)
	}
}

func TestTheContainerInfrasSecurityGroupsDescribeThemselvesInTheCharactersAWSAccepts(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	accepted := regexp.MustCompile(`^[0-9A-Za-z_ .:/()#,@\[\]+=&;{}!$*-]*$`)
	for key, inputs := range rec.recorded {
		if !strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") {
			continue
		}
		descriptions := []string{inputs["description"].StringValue()}
		for _, direction := range []resource.PropertyKey{"ingress", "egress"} {
			if !inputs[direction].IsArray() {
				continue
			}
			for _, rule := range inputs[direction].ArrayValue() {
				if description := rule.ObjectValue()["description"]; description.IsString() {
					descriptions = append(descriptions, description.StringValue())
				}
			}
		}
		for _, description := range descriptions {
			if !accepted.MatchString(description) {
				t.Errorf("%s is described %q, which AWS refuses: a security group and its rules take only %s", key, description, accepted)
			}
		}
	}
}
