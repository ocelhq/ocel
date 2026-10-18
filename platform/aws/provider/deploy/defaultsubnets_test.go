package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
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

func TestTheInfraStackLooksOnlyInTheDefaultSubnetsOfTheDefaultVPC(t *testing.T) {
	t.Parallel()

	cfg, _ := appStackSpec(t)
	release := releasing(t, cfg)
	lookups := &subnetLookups{}
	spec := provider.StackSpec{Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction}, Kind: provider.StackInfra}
	placed := &infraWork{vpc: &component.DefaultVPC{ID: "vpc-default"}, clients: "sg-vpc-clients"}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.infra(pctx, spec, placed) }, pulumi.WithMocks("shop", "prod--infra", lookups)); err != nil {
		t.Fatalf("run the infra program: %v", err)
	}

	assertDefaultSubnetsOnly(t, lookups.filtered())
}

func TestAnInfraStackWithNothingInTheVPCNeverLooksForOne(t *testing.T) {
	t.Parallel()

	cfg, _ := appStackSpec(t)
	release := releasing(t, cfg)
	lookups := &subnetLookups{}
	spec := provider.StackSpec{Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction}, Kind: provider.StackInfra}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.infra(pctx, spec, &infraWork{}) }, pulumi.WithMocks("shop", "prod--infra", lookups)); err != nil {
		t.Fatalf("run the infra program: %v", err)
	}
	if got := lookups.filtered(); len(got) != 0 {
		t.Errorf("looked subnets up %d times, want none: an account with no default VPC still deploys buckets, topics and realtime", len(got))
	}
}

func TestADatabaseInAnAccountWithNoDefaultVPCIsRefusedBeforeAnyStackRuns(t *testing.T) {
	t.Parallel()

	cfg := conformingConfig(&fakeArtifactStore{exists: map[string]bool{}})
	cfg.Network = noDefaultVPC{}
	release := releasing(t, cfg)
	spec := provider.StackSpec{
		Ref:       provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "db--main", Declared: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}
	work := &infraWork{}
	err := release.placeInVPC(context.Background(), spec, work)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "create-default-vpc") || !strings.Contains(err.Error(), "postgres main") {
		t.Fatalf("placeInVPC() = %v, want a refusal naming the database and how to make a default VPC", err)
	}
}

type noDefaultVPC struct{ defaultVPCNetwork }

func (noDefaultVPC) DescribeVpcs(context.Context, *awsec2.DescribeVpcsInput, ...func(*awsec2.Options)) (*awsec2.DescribeVpcsOutput, error) {
	return &awsec2.DescribeVpcsOutput{}, nil
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
