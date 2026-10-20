package containers

import (
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTheContainerInfraRaisesNoPrivateFrontWhenEveryAppIsRoutedPublicly(t *testing.T) {
	t.Parallel()

	infra := &work{namespace: "ocel", tier: environment.TierProduction, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags("ocel", environment.TierProduction), fronts: Fronts{Public: fixtureRanges}}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	if balancers := rec.registered("aws:lb/loadBalancer:LoadBalancer"); !slices.Equal(balancers, []string{"ocel-prod-public"}) {
		t.Errorf("declared load balancers %v, want the public one alone: nothing routes through CloudFront, and an internal one costs by the hour whether anything answers behind it or not", balancers)
	}
	if origins := rec.registered("aws:cloudfront/vpcOrigin:VpcOrigin"); len(origins) != 0 {
		t.Errorf("declared VPC origins %v, want none: no CloudFront distribution reaches this tier's containers", origins)
	}
	if groups := rec.registered("aws:ec2/securityGroup:SecurityGroup"); slices.Contains(groups, "ocel-prod-containers-sg") {
		t.Errorf("declared security groups %v, want no group for a private front that does not exist", groups)
	}
	tasks := recordedNamed(t, rec, "aws:ec2/securityGroup:SecurityGroup", "::ocel-prod-containers-tasks-sg")
	if sources := tasks["ingress"].ArrayValue()[0].ObjectValue()["securityGroups"].ArrayValue(); len(sources) != 1 {
		t.Errorf("the tasks admit %v, want the public front alone", sources)
	}
}

func TestTheContainerInfraRaisesThePrivateFrontAloneForAnAppRoutedThroughCloudFront(t *testing.T) {
	t.Parallel()

	infra := &work{namespace: "ocel", tier: environment.TierProduction, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags("ocel", environment.TierProduction), fronts: Fronts{Private: true}}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	if balancers := rec.registered("aws:lb/loadBalancer:LoadBalancer"); !slices.Equal(balancers, []string{"ocel-prod-containers"}) {
		t.Errorf("declared load balancers %v, want the internal one alone", balancers)
	}
	if origins := rec.registered("aws:cloudfront/vpcOrigin:VpcOrigin"); len(origins) != 1 {
		t.Errorf("declared VPC origins %v, want the one CloudFront reaches the internal front through", origins)
	}
}

func TestDecodeContainerInfraReadsATierWithNoPrivateFrontUnlessItExpectsOne(t *testing.T) {
	t.Parallel()

	outputs := infrastructureOutputs()
	for _, key := range []string{outputKeyListener, outputKeyOriginHost, outputKeyVPCOrigin} {
		delete(outputs, key)
	}
	decoded, err := decode(outputs, false)
	if err != nil {
		t.Fatalf("decode() of a tier with no private front = %v, want it read: every app in it is routed publicly", err)
	}
	if decoded.Listener != "" || decoded.VPCOrigin != "" || decoded.Cluster == "" {
		t.Errorf("decoded = %+v, want the cluster and no private front", decoded)
	}
	if _, err := decode(outputs, true); err == nil {
		t.Error("decode accepted outputs with no private front from a run that raised one, so a container routed through CloudFront would have no rule to hang off")
	}
	partial := infrastructureOutputs()
	delete(partial, outputKeyVPCOrigin)
	if _, err := decode(partial, false); err == nil {
		t.Error("decode accepted a private front with no VPC origin, so the edge would be handed a front it cannot reach")
	}
}
