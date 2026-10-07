package bastion

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ocelhq/ocel/pkg/environment"
)

type network struct {
	vpc     string
	cidr    string
	subnets []string
}

func Ensure(ctx context.Context, c Clients, spec Spec) (Bastion, error) {
	if spec.Boundary == "" {
		return Bastion{}, errors.New("the bastion's task role needs the app boundary of its tier, and none is named")
	}
	ports := slices.Compact(slices.Sorted(slices.Values(spec.Ports)))
	net, err := c.findDefaultNetwork(ctx)
	if err != nil {
		return Bastion{}, err
	}
	roleARN, err := c.ensureTaskRole(ctx, spec)
	if err != nil {
		return Bastion{}, err
	}
	group, err := c.ensureSecurityGroup(ctx, spec.Tier, net, ports)
	if err != nil {
		return Bastion{}, err
	}
	if err := c.ensureCluster(ctx, spec.Tier); err != nil {
		return Bastion{}, err
	}
	definition, err := c.ensureTaskDefinition(ctx, spec.Tier, roleARN)
	if err != nil {
		return Bastion{}, err
	}
	return Bastion{
		Tier:           spec.Tier,
		Cluster:        NameFor(spec.Tier),
		TaskDefinition: definition,
		SecurityGroup:  group,
		Subnets:        net.subnets,
		Ports:          ports,
	}, nil
}

func (c Clients) findDefaultNetwork(ctx context.Context) (network, error) {
	vpcs, err := c.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return network{}, fmt.Errorf("look up the default VPC: %w", err)
	}
	if len(vpcs.Vpcs) == 0 {
		return network{}, errors.New("this account has no default VPC in this region, and the bastion runs in it beside the databases and caches deploys put there")
	}
	found := network{vpc: aws.ToString(vpcs.Vpcs[0].VpcId), cidr: aws.ToString(vpcs.Vpcs[0].CidrBlock)}
	subnets, err := c.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{found.vpc}},
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	if err != nil {
		return network{}, fmt.Errorf("look up the default VPC's subnets: %w", err)
	}
	for _, subnet := range subnets.Subnets {
		found.subnets = append(found.subnets, aws.ToString(subnet.SubnetId))
	}
	slices.Sort(found.subnets)
	if len(found.subnets) == 0 {
		return network{}, fmt.Errorf("the default VPC %s has no default subnet to place the bastion in", found.vpc)
	}
	return found, nil
}

func (c Clients) ensureCluster(ctx context.Context, tier environment.Tier) error {
	name := NameFor(tier)
	described, err := c.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{name},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldTags},
	})
	if err != nil {
		return fmt.Errorf("look up cluster %s: %w", name, err)
	}
	for _, cluster := range described.Clusters {
		if aws.ToString(cluster.Status) != "ACTIVE" {
			continue
		}
		if !ecsTaggedByOcel(cluster.Tags) {
			return fmt.Errorf("cluster %s exists and is not tagged %s=%s, so Ocel will not run a bastion in it", name, managedByTagKey, managedByTagValue)
		}
		return nil
	}
	if _, err := c.ECS.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name), Tags: ecsTags(tags(tier))}); err != nil {
		return fmt.Errorf("create cluster %s: %w", name, err)
	}
	return nil
}

func (c Clients) ensureTaskDefinition(ctx context.Context, tier environment.Tier, roleARN string) (string, error) {
	family := NameFor(tier)
	described, err := c.ECS.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(family)})
	var missing *ecstypes.ClientException
	switch {
	case errors.As(err, &missing):
	case err != nil:
		return "", fmt.Errorf("look up task definition %s: %w", family, err)
	case runsTheBastion(described.TaskDefinition, roleARN):
		return aws.ToString(described.TaskDefinition.TaskDefinitionArn), nil
	}
	registered, err := c.ECS.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String(taskCPU),
		Memory:                  aws.String(taskMemoryMiB),
		TaskRoleArn:             aws.String(roleARN),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:             aws.String(containerName),
			Image:            aws.String(image),
			Essential:        aws.Bool(true),
			Command:          []string{"sh", "-c", "sleep " + taskLifetimeSeconds},
			LinuxParameters:  &ecstypes.LinuxParameters{InitProcessEnabled: aws.Bool(true)},
			WorkingDirectory: aws.String("/"),
		}},
		Tags: ecsTags(tags(tier)),
	})
	if err != nil {
		return "", fmt.Errorf("register task definition %s: %w", family, err)
	}
	return aws.ToString(registered.TaskDefinition.TaskDefinitionArn), nil
}

func runsTheBastion(definition *ecstypes.TaskDefinition, roleARN string) bool {
	if definition == nil || aws.ToString(definition.TaskRoleArn) != roleARN || definition.ExecutionRoleArn != nil || len(definition.ContainerDefinitions) != 1 {
		return false
	}
	container := definition.ContainerDefinitions[0]
	return aws.ToString(container.Name) == containerName && aws.ToString(container.Image) == image &&
		slices.Equal(container.Command, []string{"sh", "-c", "sleep " + taskLifetimeSeconds})
}

func ecsTags(tags map[string]string) []ecstypes.Tag {
	var out []ecstypes.Tag
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, ecstypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return out
}

func ecsTaggedByOcel(tags []ecstypes.Tag) bool {
	return slices.ContainsFunc(tags, func(tag ecstypes.Tag) bool {
		return aws.ToString(tag.Key) == managedByTagKey && aws.ToString(tag.Value) == managedByTagValue
	})
}
