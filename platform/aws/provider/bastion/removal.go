package bastion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
)

const deleteTaskDefinitionsBatch = 10

func Remove(ctx context.Context, c Clients, tier environment.Tier) error {
	steps := []func(context.Context, Clients, environment.Tier) error{
		removeCluster, removeSecurityGroup, removeTaskDefinitions, removeTaskRole,
	}
	for _, step := range steps {
		if err := step(ctx, c, tier); err != nil {
			return err
		}
	}
	return nil
}

func removeCluster(ctx context.Context, c Clients, tier environment.Tier) error {
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
			return fmt.Errorf("cluster %s is not tagged %s=%s, so Ocel will not delete it", name, managedByTagKey, managedByTagValue)
		}
		if err := c.stopTasks(ctx, name); err != nil {
			return err
		}
		if _, err := c.ECS.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)}); err != nil {
			return fmt.Errorf("delete cluster %s: %w", name, err)
		}
	}
	return nil
}

func (c Clients) stopTasks(ctx context.Context, cluster string) error {
	ctx, cancel := context.WithTimeout(ctx, removalTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		listed, err := c.ECS.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
		if err != nil {
			return fmt.Errorf("list the tasks of cluster %s: %w", cluster, err)
		}
		if len(listed.TaskArns) == 0 {
			return nil
		}
		for _, arn := range listed.TaskArns {
			if _, err := c.ECS.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(arn), Reason: aws.String("Ocel: the tier's bastion is being removed")}); err != nil {
				return fmt.Errorf("stop task %s: %w", arn, err)
			}
		}
		if err := c.pause(ctx, attempt); err != nil {
			return fmt.Errorf("wait for the tasks of cluster %s to stop: %w", cluster, err)
		}
	}
}

func removeSecurityGroup(ctx context.Context, c Clients, tier environment.Tier) error {
	name := NameFor(tier)
	group, err := c.findSecurityGroup(ctx, name, "")
	if err != nil || group == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, removalTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		_, err := c.EC2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: group.GroupId})
		var api smithy.APIError
		if err == nil {
			return nil
		}
		if !errors.As(err, &api) || api.ErrorCode() != "DependencyViolation" {
			return fmt.Errorf("delete security group %s: %w", name, err)
		}
		if err := c.pause(ctx, attempt); err != nil {
			return fmt.Errorf("wait for the network interfaces of security group %s to go: %w", name, err)
		}
	}
}

func removeTaskDefinitions(ctx context.Context, c Clients, tier environment.Tier) error {
	family := NameFor(tier)
	active, err := c.listRevisions(ctx, family, ecstypes.TaskDefinitionStatusActive)
	if err != nil {
		return err
	}
	for _, arn := range active {
		if _, err := c.ECS.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(arn)}); err != nil {
			return fmt.Errorf("deregister task definition %s: %w", arn, err)
		}
	}
	inactive, err := c.listRevisions(ctx, family, ecstypes.TaskDefinitionStatusInactive)
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(inactive, deleteTaskDefinitionsBatch) {
		deleted, err := c.ECS.DeleteTaskDefinitions(ctx, &ecs.DeleteTaskDefinitionsInput{TaskDefinitions: batch})
		if err != nil {
			return fmt.Errorf("delete the revisions of task definition %s: %w", family, err)
		}
		if refused := refusalOf(deleted.Failures); refused != nil {
			return fmt.Errorf("delete the revisions of task definition %s: %w", family, refused)
		}
	}
	return nil
}

func (c Clients) listRevisions(ctx context.Context, family string, status ecstypes.TaskDefinitionStatus) ([]string, error) {
	var revisions []string
	var token *string
	for {
		listed, err := c.ECS.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String(family), Status: status, NextToken: token})
		if err != nil {
			return nil, fmt.Errorf("list the %s revisions of task definition %s: %w", strings.ToLower(string(status)), family, err)
		}
		for _, arn := range listed.TaskDefinitionArns {
			if _, name, _ := strings.Cut(arn, "task-definition/"); strings.HasPrefix(name, family+":") {
				revisions = append(revisions, arn)
			}
		}
		if token = listed.NextToken; token == nil {
			return revisions, nil
		}
	}
}

func removeTaskRole(ctx context.Context, c Clients, tier environment.Tier) error {
	name := NameFor(tier)
	arn, err := c.findTaskRole(ctx, name)
	if err != nil || arn == "" {
		return err
	}
	policies, err := c.IAM.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(name)})
	if err != nil {
		return fmt.Errorf("list the policies of role %s: %w", name, err)
	}
	for _, policy := range policies.PolicyNames {
		if _, err := c.IAM.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(policy)}); err != nil {
			return fmt.Errorf("delete policy %s of role %s: %w", policy, name, err)
		}
	}
	_, err = c.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	var gone *iamtypes.NoSuchEntityException
	if err != nil && !errors.As(err, &gone) {
		return fmt.Errorf("delete role %s: %w", name, err)
	}
	return nil
}
