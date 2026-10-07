package bastion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
)

type peer int

const (
	ipv4Range peer = iota
	ipv6Range
	prefixList
	securityGroup
)

type rule struct {
	protocol string
	from     int32
	to       int32
	peer     peer
	address  string
}

func (r rule) permission() ec2types.IpPermission {
	permission := ec2types.IpPermission{IpProtocol: aws.String(r.protocol)}
	if r.protocol != "-1" {
		permission.FromPort, permission.ToPort = aws.Int32(r.from), aws.Int32(r.to)
	}
	switch r.peer {
	case ipv4Range:
		permission.IpRanges = []ec2types.IpRange{{CidrIp: aws.String(r.address)}}
	case ipv6Range:
		permission.Ipv6Ranges = []ec2types.Ipv6Range{{CidrIpv6: aws.String(r.address)}}
	case prefixList:
		permission.PrefixListIds = []ec2types.PrefixListId{{PrefixListId: aws.String(r.address)}}
	case securityGroup:
		permission.UserIdGroupPairs = []ec2types.UserIdGroupPair{{GroupId: aws.String(r.address)}}
	}
	return permission
}

func rulesOf(permissions []ec2types.IpPermission) []rule {
	var rules []rule
	for _, permission := range permissions {
		protocol, from, to := aws.ToString(permission.IpProtocol), aws.ToInt32(permission.FromPort), aws.ToInt32(permission.ToPort)
		for _, ipRange := range permission.IpRanges {
			rules = append(rules, rule{protocol, from, to, ipv4Range, aws.ToString(ipRange.CidrIp)})
		}
		for _, ipRange := range permission.Ipv6Ranges {
			rules = append(rules, rule{protocol, from, to, ipv6Range, aws.ToString(ipRange.CidrIpv6)})
		}
		for _, list := range permission.PrefixListIds {
			rules = append(rules, rule{protocol, from, to, prefixList, aws.ToString(list.PrefixListId)})
		}
		for _, pair := range permission.UserIdGroupPairs {
			rules = append(rules, rule{protocol, from, to, securityGroup, aws.ToString(pair.GroupId)})
		}
	}
	return rules
}

func egressRules(net network, ports []int) []rule {
	rules := []rule{{protocol: "tcp", from: httpsPort, to: httpsPort, peer: ipv4Range, address: anywhere}}
	for _, port := range ports {
		rules = append(rules, rule{protocol: "tcp", from: int32(port), to: int32(port), peer: ipv4Range, address: net.cidr})
	}
	return rules
}

func (c Clients) reconcileSecurityGroup(ctx context.Context, tier environment.Tier, net network, ports []int) (string, error) {
	name := NameFor(tier)
	group, err := c.findSecurityGroup(ctx, name, net.vpc)
	if err != nil {
		return "", err
	}
	if group == nil {
		if group, err = c.createSecurityGroup(ctx, tier, net.vpc); err != nil {
			return "", err
		}
	}
	id := aws.ToString(group.GroupId)
	if err := c.closeIngress(ctx, id, group); err != nil {
		return "", err
	}
	return id, c.limitEgress(ctx, id, group, egressRules(net, ports))
}

func (c Clients) findSecurityGroup(ctx context.Context, name, vpc string) (*ec2types.SecurityGroup, error) {
	filters := []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{name}}}
	if vpc != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("vpc-id"), Values: []string{vpc}})
	}
	described, err := c.EC2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("look up security group %s: %w", name, err)
	}
	if len(described.SecurityGroups) == 0 {
		return nil, nil
	}
	group := described.SecurityGroups[0]
	if !isManagedByOcel(group.Tags, func(tag ec2types.Tag) (*string, *string) { return tag.Key, tag.Value }) {
		return nil, refuseUnowned("security group", name)
	}
	return &group, nil
}

func (c Clients) createSecurityGroup(ctx context.Context, tier environment.Tier, vpc string) (*ec2types.SecurityGroup, error) {
	name := NameFor(tier)
	_, err := c.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:         aws.String(name),
		Description:       aws.String("Ocel: the " + string(tier) + " tier's bastion, which nothing reaches and which reaches only HTTPS and the ports of its databases and caches"),
		VpcId:             aws.String(vpc),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: ec2TagsFor(tier)}},
	})
	var api smithy.APIError
	duplicate := errors.As(err, &api) && api.ErrorCode() == "InvalidGroup.Duplicate"
	if err != nil && !duplicate {
		return nil, fmt.Errorf("create security group %s: %w", name, err)
	}
	created, err := c.findSecurityGroup(ctx, name, vpc)
	if err != nil {
		return nil, err
	}
	if created == nil {
		return nil, fmt.Errorf("security group %s was created and is not there", name)
	}
	return created, nil
}

func (c Clients) closeIngress(ctx context.Context, id string, group *ec2types.SecurityGroup) error {
	var open []ec2types.IpPermission
	for _, admitted := range rulesOf(group.IpPermissions) {
		open = append(open, admitted.permission())
	}
	if len(open) == 0 {
		return nil
	}
	if _, err := c.EC2.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: aws.String(id), IpPermissions: open}); err != nil {
		return fmt.Errorf("close the ingress of security group %s: %w", id, err)
	}
	return nil
}

func (c Clients) limitEgress(ctx context.Context, id string, group *ec2types.SecurityGroup, want []rule) error {
	have := rulesOf(group.IpPermissionsEgress)
	var missing, extra []ec2types.IpPermission
	for _, wanted := range want {
		if !slices.Contains(have, wanted) {
			missing = append(missing, wanted.permission())
		}
	}
	for _, held := range have {
		if !slices.Contains(want, held) {
			extra = append(extra, held.permission())
		}
	}
	if len(missing) > 0 {
		if err := c.openEgress(ctx, id, missing); err != nil {
			return err
		}
	}
	if len(extra) > 0 {
		if _, err := c.EC2.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: aws.String(id), IpPermissions: extra}); err != nil {
			return fmt.Errorf("close egress %s on security group %s: %w", describeRules(extra), id, err)
		}
	}
	return nil
}

func (c Clients) openEgress(ctx context.Context, id string, permissions []ec2types.IpPermission) error {
	_, err := c.EC2.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(id), IpPermissions: permissions})
	if isDuplicateRule(err) {
		for _, permission := range permissions {
			_, err = c.EC2.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(id), IpPermissions: []ec2types.IpPermission{permission}})
			if err != nil && !isDuplicateRule(err) {
				break
			}
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("open egress %s on security group %s: %w", describeRules(permissions), id, err)
	}
	return nil
}

func isDuplicateRule(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidPermission.Duplicate"
}

func describeRules(permissions []ec2types.IpPermission) string {
	var out []string
	for _, described := range rulesOf(permissions) {
		out = append(out, described.protocol+"/"+strconv.Itoa(int(described.from))+"-"+strconv.Itoa(int(described.to))+" "+described.address)
	}
	return fmt.Sprint(out)
}
