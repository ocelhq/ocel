package vpcclients

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	Part    = "vpc-clients"
	Summary = "the security group every function, task and bastion that reaches a database or cache of the tier joins, and the only one their ingress admits"

	KindSecurityGroup = "AWS::EC2::SecurityGroup"

	clientsWord = "vpc-clients"
)

type API interface {
	component.VPCsAPI
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error)
}

func NameFor(namespace string, tier environment.Tier) string {
	return naming.OcelName(namespace, tier, clientsWord)
}

func Digest(namespace string, tier environment.Tier) string {
	sum := sha256.Sum256([]byte(NameFor(namespace, tier) + "\n" + Summary))
	return hex.EncodeToString(sum[:])
}

func GroupOf(resources []provider.PartResource) string {
	for _, resource := range resources {
		if resource.Kind == KindSecurityGroup {
			return resource.ID
		}
	}
	return ""
}

func Ensure(ctx context.Context, api API, namespace string, tier environment.Tier, region string) ([]provider.PartResource, error) {
	vpc, err := component.ReadDefaultVPC(ctx, api, region, "the databases, caches and bastion this security group admits")
	if err != nil {
		return nil, err
	}
	name := NameFor(namespace, tier)
	found, err := api.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpc.ID}},
			{Name: aws.String("group-name"), Values: []string{name}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("look up the %s security group: %w", name, err)
	}
	if len(found.SecurityGroups) > 0 {
		return []provider.PartResource{{Kind: KindSecurityGroup, ID: aws.ToString(found.SecurityGroups[0].GroupId)}}, nil
	}
	created, err := api.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String("Ocel " + string(tier) + ": what joins it may reach the tier's databases and caches; it admits nothing itself"),
		VpcId:       aws.String(vpc.ID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags: []ec2types.Tag{
				{Key: aws.String("Name"), Value: aws.String(name)},
				{Key: aws.String(naming.ManagedByTagKey), Value: aws.String(namespace)},
				{Key: aws.String(naming.EnvTierTagKey), Value: aws.String(string(tier))},
			},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("create the %s security group: %w", name, err)
	}
	return []provider.PartResource{{Kind: KindSecurityGroup, ID: aws.ToString(created.GroupId)}}, nil
}

func Remove(ctx context.Context, api API, recorded []provider.PartResource) error {
	group := GroupOf(recorded)
	if group == "" {
		return nil
	}
	_, err := api.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(group)})
	var apiErr smithy.APIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidGroup.NotFound":
		return nil
	case errors.As(err, &apiErr) && apiErr.ErrorCode() == "DependencyViolation":
		return fmt.Errorf("security group %s is still joined or admitted by a database, cache, function, task or bastion of the tier: destroy those first", group)
	}
	return fmt.Errorf("delete security group %s: %w", group, err)
}
