package bastion

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ocelhq/ocel/pkg/environment"
)

func Reconcile(ctx context.Context, c Clients, spec Spec) (Bastion, error) {
	if spec.Boundary == "" {
		return Bastion{}, errors.New("the bastion's task role needs the app boundary of its tier, and none is named")
	}
	net := spec.Network
	if net.VPC == "" || net.CIDR == "" || len(net.Subnets) == 0 {
		return Bastion{}, errors.New("the bastion runs in a public subnet of its tier's network, and none is named")
	}
	roleARN, err := c.ensureTaskRole(ctx, spec)
	if err != nil {
		return Bastion{}, err
	}
	group, err := c.reconcileSecurityGroup(ctx, spec.Namespace, spec.Tier, net, spec.Ports)
	if err != nil {
		return Bastion{}, err
	}
	if err := c.ensureCluster(ctx, spec.Namespace, spec.Tier); err != nil {
		return Bastion{}, err
	}
	definition, err := c.reconcileTaskDefinition(ctx, spec.Namespace, spec.Tier, roleARN)
	if err != nil {
		return Bastion{}, err
	}
	return Bastion{
		Tier:           spec.Tier,
		Cluster:        NameFor(spec.Namespace, spec.Tier),
		TaskDefinition: definition,
		Clients:        net.Clients,
		SecurityGroup:  group,
		Subnets:        net.Subnets,
		Ports:          spec.Ports,
	}, nil
}

func (c Clients) ensureCluster(ctx context.Context, namespace string, tier environment.Tier) error {
	name := NameFor(namespace, tier)
	found, err := c.findCluster(ctx, namespace, name)
	if err != nil || found {
		return err
	}
	if err := c.ensureLinkedRole(ctx); err != nil {
		return err
	}
	if _, err := c.ECS.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name), Tags: ecsTagsFor(namespace, tier)}); err != nil {
		return fmt.Errorf("create cluster %s: %w", name, err)
	}
	return nil
}

func (c Clients) findCluster(ctx context.Context, namespace, name string) (bool, error) {
	described, err := c.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{name},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldTags},
	})
	if err != nil {
		return false, fmt.Errorf("look up cluster %s: %w", name, err)
	}
	for _, cluster := range described.Clusters {
		if aws.ToString(cluster.Status) != "ACTIVE" {
			continue
		}
		if !isManagedByOcel(namespace, cluster.Tags, func(tag ecstypes.Tag) (*string, *string) { return tag.Key, tag.Value }) {
			return false, refuseUnowned(namespace, "cluster", name)
		}
		return true, nil
	}
	return false, nil
}

func (c Clients) reconcileTaskDefinition(ctx context.Context, namespace string, tier environment.Tier, roleARN string) (string, error) {
	family := NameFor(namespace, tier)
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
		Tags: ecsTagsFor(namespace, tier),
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
	if definition.NetworkMode != ecstypes.NetworkModeAwsvpc || !slices.Contains(definition.RequiresCompatibilities, ecstypes.CompatibilityFargate) ||
		aws.ToString(definition.Cpu) != taskCPU || aws.ToString(definition.Memory) != taskMemoryMiB {
		return false
	}
	container := definition.ContainerDefinitions[0]
	return aws.ToString(container.Name) == containerName && aws.ToString(container.Image) == image &&
		slices.Equal(container.Command, []string{"sh", "-c", "sleep " + taskLifetimeSeconds})
}
