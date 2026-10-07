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

type rule struct {
	protocol string
	from     int32
	to       int32
	kind     string
	target   string
}

func (r rule) permission() ec2types.IpPermission {
	permission := ec2types.IpPermission{IpProtocol: aws.String(r.protocol)}
	if r.protocol != "-1" {
		permission.FromPort, permission.ToPort = aws.Int32(r.from), aws.Int32(r.to)
	}
	switch r.kind {
	case "cidr":
		permission.IpRanges = []ec2types.IpRange{{CidrIp: aws.String(r.target)}}
	case "cidr6":
		permission.Ipv6Ranges = []ec2types.Ipv6Range{{CidrIpv6: aws.String(r.target)}}
	case "prefix-list":
		permission.PrefixListIds = []ec2types.PrefixListId{{PrefixListId: aws.String(r.target)}}
	case "group":
		permission.UserIdGroupPairs = []ec2types.UserIdGroupPair{{GroupId: aws.String(r.target)}}
	}
	return permission
}

func rulesOf(permissions []ec2types.IpPermission) []rule {
	var rules []rule
	for _, p := range permissions {
		base := rule{protocol: aws.ToString(p.IpProtocol), from: aws.ToInt32(p.FromPort), to: aws.ToInt32(p.ToPort)}
		for _, r := range p.IpRanges {
			rules = append(rules, rule{base.protocol, base.from, base.to, "cidr", aws.ToString(r.CidrIp)})
		}
		for _, r := range p.Ipv6Ranges {
			rules = append(rules, rule{base.protocol, base.from, base.to, "cidr6", aws.ToString(r.CidrIpv6)})
		}
		for _, r := range p.PrefixListIds {
			rules = append(rules, rule{base.protocol, base.from, base.to, "prefix-list", aws.ToString(r.PrefixListId)})
		}
		for _, r := range p.UserIdGroupPairs {
			rules = append(rules, rule{base.protocol, base.from, base.to, "group", aws.ToString(r.GroupId)})
		}
	}
	return rules
}

func egressRules(net network, ports []int) []rule {
	rules := []rule{{protocol: "tcp", from: httpsPort, to: httpsPort, kind: "cidr", target: anywhere}}
	for _, port := range ports {
		rules = append(rules, rule{protocol: "tcp", from: int32(port), to: int32(port), kind: "cidr", target: net.cidr})
	}
	return rules
}

func (c Clients) ensureSecurityGroup(ctx context.Context, tier environment.Tier, net network, ports []int) (string, error) {
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
	if !slices.ContainsFunc(group.Tags, func(tag ec2types.Tag) bool {
		return aws.ToString(tag.Key) == managedByTagKey && aws.ToString(tag.Value) == managedByTagValue
	}) {
		return nil, fmt.Errorf("security group %s exists and is not tagged %s=%s, so Ocel will not run a bastion behind it", name, managedByTagKey, managedByTagValue)
	}
	return &group, nil
}

func (c Clients) createSecurityGroup(ctx context.Context, tier environment.Tier, vpc string) (*ec2types.SecurityGroup, error) {
	name := NameFor(tier)
	var groupTags []ec2types.Tag
	for _, tag := range ecsTags(tags(tier)) {
		groupTags = append(groupTags, ec2types.Tag{Key: tag.Key, Value: tag.Value})
	}
	_, err := c.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:         aws.String(name),
		Description:       aws.String("Ocel: the " + string(tier) + " tier's bastion, which nothing reaches and which reaches only HTTPS and the ports of its databases and caches"),
		VpcId:             aws.String(vpc),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: groupTags}},
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
	for _, r := range rulesOf(group.IpPermissions) {
		open = append(open, r.permission())
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
	for _, r := range want {
		if !slices.Contains(have, r) {
			missing = append(missing, r.permission())
		}
	}
	for _, r := range have {
		if !slices.Contains(want, r) {
			extra = append(extra, r.permission())
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
	for _, r := range rulesOf(permissions) {
		out = append(out, r.protocol+"/"+strconv.Itoa(int(r.from))+"-"+strconv.Itoa(int(r.to))+" "+r.target)
	}
	return fmt.Sprint(out)
}
