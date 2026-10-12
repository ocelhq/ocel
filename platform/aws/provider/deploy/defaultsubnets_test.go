package deploy

import (
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type subnetLookups struct {
	inputRecorder
	lookupMu sync.Mutex
	filters  [][]string
}

func (s *subnetLookups) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if args.Token != "aws:ec2/getSubnets:getSubnets" {
		return s.inputRecorder.Call(args)
	}
	var seen []string
	for _, filter := range args.Args["filters"].ArrayValue() {
		fields := filter.ObjectValue()
		seen = append(seen, fields["name"].StringValue())
	}
	s.lookupMu.Lock()
	s.filters = append(s.filters, seen)
	s.lookupMu.Unlock()
	return resource.PropertyMap{}, nil
}

func (s *subnetLookups) filtered() [][]string {
	s.lookupMu.Lock()
	defer s.lookupMu.Unlock()
	return slices.Clone(s.filters)
}

func TestTheContainerInfraLooksOnlyInTheDefaultSubnetsOfTheDefaultVPC(t *testing.T) {
	t.Parallel()

	work := &containerInfraWork{tier: environment.TierProduction, boundary: testBoundaryARN, tags: containerInfraTags(environment.TierProduction)}
	lookups := &subnetLookups{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", lookups)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	assertDefaultSubnetsOnly(t, lookups.filtered())
}

func TestTheInfraStackLooksOnlyInTheDefaultSubnetsOfTheDefaultVPC(t *testing.T) {
	t.Parallel()

	cfg, _ := appStackSpec(t)
	release := releasing(t, cfg)
	lookups := &subnetLookups{}
	spec := provider.StackSpec{Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction}, Kind: provider.StackInfra}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.infra(pctx, spec, &infraWork{}) }, pulumi.WithMocks("shop", "prod--infra", lookups)); err != nil {
		t.Fatalf("run the infra program: %v", err)
	}

	assertDefaultSubnetsOnly(t, lookups.filtered())
}

func assertDefaultSubnetsOnly(t *testing.T, filters [][]string) {
	t.Helper()
	if len(filters) != 1 {
		t.Fatalf("looked subnets up %d times, want once", len(filters))
	}
	if !slices.Contains(filters[0], "default-for-az") {
		t.Errorf("subnet lookup filtered on %v, want default-for-az too: a private subnet Ocel adds to the VPC must never enter a database subnet group, a cluster or a load balancer", filters[0])
	}
}
