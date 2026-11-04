package network

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/quotas"
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
	if len(reachable) < sharedparts.NetworkZones {
		return nil, refusal.Refuse(refusal.CodeUnsupported,
			"this region has %d availability zones a CloudFront VPC origin reaches (%s), and ocel spreads a tier's network across %d",
			len(reachable), strings.Join(reachable, ", "), sharedparts.NetworkZones)
	}
	return reachable[:sharedparts.NetworkZones], nil
}

type Placement struct {
	Region    string
	AccountID string
	Namespace string
	Tier      environment.Tier
	CIDR      string
}

type heldBlock struct {
	vpc   string
	block netip.Prefix
}

type otherVPCs struct {
	owned  int
	blocks []heldBlock
}

func Place(ctx context.Context, region RegionAPI, limits quotas.API, at Placement) (Layout, error) {
	others, err := readOtherVPCs(ctx, region, at)
	if err != nil {
		return Layout{}, err
	}
	cidr, err := others.pick(at)
	if err != nil {
		return Layout{}, err
	}
	if err := quotas.RefuseFull(ctx, limits, quotas.VPC, at.Region, others.owned, 1, "the "+string(at.Tier)+" tier's network"); err != nil {
		return Layout{}, err
	}
	return NewLayout(cidr)
}

func readOtherVPCs(ctx context.Context, region RegionAPI, at Placement) (otherVPCs, error) {
	var others otherVPCs
	pages := ec2.NewDescribeVpcsPaginator(region, &ec2.DescribeVpcsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return otherVPCs{}, fmt.Errorf("list the VPCs in %s: %w", at.Region, err)
		}
		for _, vpc := range page.Vpcs {
			if isNetworkOf(vpc, at.Namespace, at.Tier) {
				continue
			}
			if aws.ToString(vpc.OwnerId) == at.AccountID {
				others.owned++
			}
			for _, block := range blocksOf(vpc) {
				if parsed, err := netip.ParsePrefix(block); err == nil {
					others.blocks = append(others.blocks, heldBlock{vpc: aws.ToString(vpc.VpcId), block: parsed})
				}
			}
		}
	}
	return others, nil
}

func (o otherVPCs) overlapping(ours netip.Prefix) (heldBlock, bool) {
	for _, held := range o.blocks {
		if held.block.Overlaps(ours) {
			return held, true
		}
	}
	return heldBlock{}, false
}

func (o otherVPCs) pick(at Placement) (string, error) {
	if at.CIDR != "" {
		block, err := netip.ParsePrefix(at.CIDR)
		if err != nil {
			return "", fmt.Errorf("read the network's block %q: %w", at.CIDR, err)
		}
		if held, overlaps := o.overlapping(block); overlaps {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"the %s tier's network would take %s, and %s in %s already holds %s: set the network's `vpc.cidrBlock` in a transform to a private block clear of it",
				at.Tier, at.CIDR, held.vpc, at.Region, held.block)
		}
		return at.CIDR, nil
	}
	candidates := DefaultCIDRs(at.Namespace, at.Tier)
	for _, candidate := range candidates {
		if _, overlaps := o.overlapping(netip.MustParsePrefix(candidate)); !overlaps {
			return candidate, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"every one of the %d blocks the %s tier's network defaults to overlaps a VPC already in %s: set the network's `vpc.cidrBlock` in a transform to a private block clear of them",
		len(candidates), at.Tier, at.Region)
}

func isNetworkOf(vpc ec2types.Vpc, namespace string, tier environment.Tier) bool {
	return component.HasTags(vpc.Tags, projectTags(namespace, tier))
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
