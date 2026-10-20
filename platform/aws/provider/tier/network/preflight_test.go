package network

import (
	"context"
	"errors"
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

func TestANetworkWhoseBlockOverlapsAVPCAlreadyInTheRegionIsRefusedNamingItAndTheOption(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{quota: 5, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16"), vpc("vpc-theirs", "192.168.0.0/16", "10.20.128.0/17")}}
	err := RefusePlacement(context.Background(), region, region, "us-east-1", "production", "10.20.0.0/16")
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("RefusePlacement() = %v, want a refusal", err)
	}
	for _, named := range []string{"vpc-theirs", "10.20.128.0/17", "network.production.cidr"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("refusal %q does not name %q", refused.Message, named)
		}
	}
	if err := RefusePlacement(context.Background(), region, region, "us-east-1", "production", "10.30.0.0/16"); err != nil {
		t.Errorf("RefusePlacement() = %v, want a block clear of every VPC accepted", err)
	}
}

func TestANetworkInARegionAtItsVPCQuotaIsRefusedNamingTheQuota(t *testing.T) {
	t.Parallel()

	region := &fakeRegion{quota: 2, vpcs: []ec2types.Vpc{vpc("vpc-default", "172.31.0.0/16"), vpc("vpc-theirs", "192.168.0.0/16")}}
	err := RefusePlacement(context.Background(), region, region, "us-east-1", "production", "10.20.0.0/16")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-F678F1CE") {
		t.Fatalf("RefusePlacement() = %v, want a refusal naming quota L-F678F1CE", err)
	}
}
