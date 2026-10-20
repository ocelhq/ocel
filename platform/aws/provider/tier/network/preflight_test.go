package network

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type fakeRegion struct {
	zones  []ec2types.AvailabilityZone
	vpcs   []ec2types.Vpc
	quota  float64
	zoneIn *ec2.DescribeAvailabilityZonesInput
}

func (f *fakeRegion) DescribeAvailabilityZones(_ context.Context, in *ec2.DescribeAvailabilityZonesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error) {
	f.zoneIn = in
	return &ec2.DescribeAvailabilityZonesOutput{AvailabilityZones: f.zones}, nil
}

func (f *fakeRegion) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

func (f *fakeRegion) GetServiceQuota(context.Context, *servicequotas.GetServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(f.quota)}}, nil
}

func (f *fakeRegion) GetAWSDefaultServiceQuota(context.Context, *servicequotas.GetAWSDefaultServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetAWSDefaultServiceQuotaOutput, error) {
	return &servicequotas.GetAWSDefaultServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(f.quota)}}, nil
}

func zone(id string) ec2types.AvailabilityZone {
	return ec2types.AvailabilityZone{ZoneId: aws.String(id), ZoneName: aws.String("name-of-" + id)}
}

func vpc(id string, blocks ...string) ec2types.Vpc {
	described := ec2types.Vpc{VpcId: aws.String(id), CidrBlock: aws.String(blocks[0])}
	for _, block := range blocks {
		described.CidrBlockAssociationSet = append(described.CidrBlockAssociationSet, ec2types.VpcCidrBlockAssociation{
			CidrBlock:      aws.String(block),
			CidrBlockState: &ec2types.VpcCidrBlockState{State: ec2types.VpcCidrBlockStateCodeAssociated},
		})
	}
	return described
}

func TestTheNetworkTakesTheFirstTwoZonesByIDThatAVPCOriginReaches(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{zones: []ec2types.AvailabilityZone{zone("use1-az4"), zone("use1-az3"), zone("use1-az6"), zone("use1-az1")}}
	zones, err := PickZones(context.Background(), region)
	if err != nil {
		t.Fatalf("PickZones() = %v", err)
	}
	if !slices.Equal(zones, []string{"use1-az1", "use1-az4"}) {
		t.Errorf("PickZones() = %v, want use1-az1 and use1-az4: use1-az3 is one CloudFront VPC origins cannot reach", zones)
	}
	filters := map[string][]string{}
	for _, filter := range region.zoneIn.Filters {
		filters[aws.ToString(filter.Name)] = filter.Values
	}
	if !slices.Equal(filters["zone-type"], []string{"availability-zone"}) || !slices.Equal(filters["state"], []string{"available"}) {
		t.Errorf("zones filtered by %v, want available availability zones only, not local or wavelength zones", filters)
	}
}

func TestARegionWithFewerThanTwoReachableZonesIsRefused(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{zones: []ec2types.AvailabilityZone{zone("use1-az3"), zone("use1-az1")}}
	_, err := PickZones(context.Background(), region)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "use1-az1") {
		t.Fatalf("PickZones() = %v, want a refusal naming the one zone it found", err)
	}
}

func placing(configured string) Placement {
	return Placement{Region: "us-east-1", Namespace: "ocel", Tier: "production", Configured: configured}
}

func TestANetworkWhoseConfiguredBlockOverlapsAVPCAlreadyInTheRegionIsRefusedNamingItAndTheOption(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16"), vpc("vpc-theirs", "192.168.0.0/16", "10.20.128.0/17")}}
	_, err := Place(context.Background(), region, region, placing("10.20.0.0/16"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("Place() = %v, want a refusal", err)
	}
	for _, named := range []string{"vpc-theirs", "10.20.128.0/17", "network.production.cidr"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("refusal %q does not name %q", refused.Message, named)
		}
	}
	if layout, err := Place(context.Background(), region, region, placing("10.30.0.0/16")); err != nil || layout.CIDR != "10.30.0.0/16" {
		t.Errorf("Place() = %+v, %v, want the configured block clear of every VPC taken", layout, err)
	}
}

func TestANetworkWithNoConfiguredBlockTakesTheFirstDefaultBlockNoOtherVPCOverlaps(t *testing.T) {
	t.Parallel()

	candidates := DefaultCIDRs("ocel", "production")
	first, second := netip.MustParsePrefix(candidates[0]), netip.MustParsePrefix(candidates[1])
	region := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{
		vpc("vpc-same-default", candidates[0]),
		vpc("vpc-partly-in-the-way", netip.PrefixFrom(second.Addr(), 20).String()),
	}}
	layout, err := Place(context.Background(), region, region, placing(""))
	if err != nil || layout.CIDR != candidates[2] {
		t.Fatalf("Place() = %+v, %v, want %s: %s and %s each overlap a VPC in the region", layout, err, candidates[2], first, second)
	}

	clear := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16")}}
	if layout, err := Place(context.Background(), clear, clear, placing("")); err != nil || layout.CIDR != candidates[0] {
		t.Errorf("Place() = %+v, %v, want the namespace's own default %s when nothing holds it", layout, err, candidates[0])
	}
}

func TestANetworkWithNoConfiguredBlockIsRefusedNamingTheOptionWhenEveryDefaultBlockIsHeld(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{vpc("vpc-everything", "10.0.0.0/8")}}
	_, err := Place(context.Background(), region, region, placing(""))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "network.production.cidr") {
		t.Fatalf("Place() = %v, want a refusal naming the option that sets a block", err)
	}
}

func taggedAs(described ec2types.Vpc, tags map[string]string) ec2types.Vpc {
	for key, value := range tags {
		described.Tags = append(described.Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return described
}

func TestARetriedNetworkIsNotRefusedForTheVPCItsOwnFailedRaiseLeftBehind(t *testing.T) {
	t.Parallel()

	leftover := taggedAs(vpc("vpc-leftover", "10.20.0.0/16"), stackTags("ocel", "production"))
	region := &fakeRegion{quota: 2, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16"), leftover}}
	if _, err := Place(context.Background(), region, region, placing("10.20.0.0/16")); err != nil {
		t.Errorf("Place() = %v, want the network's own VPC neither in the way nor counted against the quota: the retry takes it over", err)
	}

	for _, theirs := range []ec2types.Vpc{
		taggedAs(vpc("vpc-other-namespace", "10.20.0.0/16"), stackTags("acme", "production")),
		taggedAs(vpc("vpc-other-tier", "10.20.0.0/16"), stackTags("ocel", "preview")),
	} {
		region := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{theirs}}
		_, err := Place(context.Background(), region, region, placing("10.20.0.0/16"))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, aws.ToString(theirs.VpcId)) {
			t.Errorf("Place() = %v, want a refusal naming %s, a network of another namespace or tier", err, aws.ToString(theirs.VpcId))
		}
	}
}

func TestANetworkInARegionAtItsVPCQuotaIsRefusedNamingTheQuota(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{quota: 2, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16"), vpc("vpc-theirs", "192.168.0.0/16")}}
	_, err := Place(context.Background(), region, region, placing("10.20.0.0/16"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-F678F1CE") {
		t.Fatalf("Place() = %v, want a refusal naming quota L-F678F1CE", err)
	}
}
