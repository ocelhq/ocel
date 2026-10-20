package network

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

var VPCOriginUnsupportedZones = []string{"use1-az3", "usw1-az2", "apne1-az3", "cac1-az3"}

var releasedBlockStates = []ec2types.VpcCidrBlockStateCode{ec2types.VpcCidrBlockStateCodeDisassociated, ec2types.VpcCidrBlockStateCodeFailed}

type ZonesAPI interface {
	DescribeAvailabilityZones(ctx context.Context, in *ec2.DescribeAvailabilityZonesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error)
}

type RegionAPI interface {
	ZonesAPI
	ec2.DescribeVpcsAPIClient
}

func PickZones(ctx context.Context, api ZonesAPI) ([]string, error) {
	described, err := api.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("zone-type"), Values: []string{"availability-zone"}},
			{Name: aws.String("state"), Values: []string{"available"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list the region's availability zones: %w", err)
	}
	var reachable []string
	for _, zone := range described.AvailabilityZones {
		id := aws.ToString(zone.ZoneId)
		if id != "" && !slices.Contains(VPCOriginUnsupportedZones, id) {
			reachable = append(reachable, id)
		}
	}
	slices.Sort(reachable)
	if len(reachable) < Zones {
		return nil, refusal.Refuse(refusal.CodeUnsupported,
			"this region has %d availability zones a CloudFront VPC origin reaches (%s), and ocel spreads a tier's network across %d",
			len(reachable), strings.Join(reachable, ", "), Zones)
	}
	return reachable[:Zones], nil
}

func RefusePlacement(ctx context.Context, region RegionAPI, quotas component.QuotasAPI, regionName string, tier environment.Tier, cidr string) error {
	ours, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("read the network's block %q: %w", cidr, err)
	}
	held := 0
	pages := ec2.NewDescribeVpcsPaginator(region, &ec2.DescribeVpcsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list the VPCs in %s: %w", regionName, err)
		}
		for _, vpc := range page.Vpcs {
			held++
			for _, block := range blocksOf(vpc) {
				theirs, err := netip.ParsePrefix(block)
				if err != nil || !theirs.Overlaps(ours) {
					continue
				}
				return refusal.Refuse(refusal.CodeInvalid,
					"the %s tier's network would take %s, and %s in %s already holds %s: set `network.%s.cidr` in the provider options to a private block clear of it",
					tier, cidr, aws.ToString(vpc.VpcId), regionName, block, tier)
			}
		}
	}
	return component.RefuseFullQuota(ctx, quotas, component.VPCQuota, regionName, held, 1, "the "+string(tier)+" tier's network")
}

func blocksOf(vpc ec2types.Vpc) []string {
	var blocks []string
	for _, association := range vpc.CidrBlockAssociationSet {
		if association.CidrBlockState != nil && slices.Contains(releasedBlockStates, association.CidrBlockState.State) {
			continue
		}
		blocks = append(blocks, aws.ToString(association.CidrBlock))
	}
	if len(blocks) == 0 && vpc.CidrBlock != nil {
		blocks = append(blocks, aws.ToString(vpc.CidrBlock))
	}
	return blocks
}
