package bastion

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

type ECSAPI interface {
	CreateCluster(ctx context.Context, in *ecs.CreateClusterInput, optFns ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error)
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, optFns ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	DeleteCluster(ctx context.Context, in *ecs.DeleteClusterInput, optFns ...func(*ecs.Options)) (*ecs.DeleteClusterOutput, error)
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	ListTaskDefinitions(ctx context.Context, in *ecs.ListTaskDefinitionsInput, optFns ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error)
	DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error)
	DeleteTaskDefinitions(ctx context.Context, in *ecs.DeleteTaskDefinitionsInput, optFns ...func(*ecs.Options)) (*ecs.DeleteTaskDefinitionsOutput, error)
	RunTask(ctx context.Context, in *ecs.RunTaskInput, optFns ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListTasks(ctx context.Context, in *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	StopTask(ctx context.Context, in *ecs.StopTaskInput, optFns ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
}

type IAMAPI interface {
	GetRole(ctx context.Context, in *iam.GetRoleInput, optFns ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	CreateRole(ctx context.Context, in *iam.CreateRoleInput, optFns ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	GetRolePolicy(ctx context.Context, in *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
	PutRolePolicy(ctx context.Context, in *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	ListRolePolicies(ctx context.Context, in *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	DeleteRolePolicy(ctx context.Context, in *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	DeleteRole(ctx context.Context, in *iam.DeleteRoleInput, optFns ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

type EC2API interface {
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	AuthorizeSecurityGroupEgress(ctx context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error)
	RevokeSecurityGroupEgress(ctx context.Context, in *ec2.RevokeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error)
	RevokeSecurityGroupIngress(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error)
	DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error)
}

type Clients struct {
	ECS ECSAPI
	IAM IAMAPI
	EC2 EC2API
}

func NewClients(cfg aws.Config) Clients {
	return Clients{ECS: ecs.NewFromConfig(cfg), IAM: iam.NewFromConfig(cfg), EC2: newEC2Client(ec2.NewFromConfig(cfg))}
}

type ec2Client struct {
	describeSecurityGroups       func(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	createSecurityGroup          func(ctx context.Context, in *ec2.CreateSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	authorizeSecurityGroupEgress func(ctx context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error)
	revokeSecurityGroupEgress    func(ctx context.Context, in *ec2.RevokeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error)
	revokeSecurityGroupIngress   func(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error)
	deleteSecurityGroup          func(ctx context.Context, in *ec2.DeleteSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error)
}

func newEC2Client(client *ec2.Client) ec2Client {
	return ec2Client{
		describeSecurityGroups:       client.DescribeSecurityGroups,
		createSecurityGroup:          client.CreateSecurityGroup,
		authorizeSecurityGroupEgress: client.AuthorizeSecurityGroupEgress,
		revokeSecurityGroupEgress:    client.RevokeSecurityGroupEgress,
		revokeSecurityGroupIngress:   client.RevokeSecurityGroupIngress,
		deleteSecurityGroup:          client.DeleteSecurityGroup,
	}
}

func (c ec2Client) DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return c.describeSecurityGroups(ctx, in, optFns...)
}

func (c ec2Client) CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	return c.createSecurityGroup(ctx, in, optFns...)
}

func (c ec2Client) AuthorizeSecurityGroupEgress(ctx context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	return c.authorizeSecurityGroupEgress(ctx, in, optFns...)
}

func (c ec2Client) RevokeSecurityGroupEgress(ctx context.Context, in *ec2.RevokeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	return c.revokeSecurityGroupEgress(ctx, in, optFns...)
}

func (c ec2Client) RevokeSecurityGroupIngress(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	return c.revokeSecurityGroupIngress(ctx, in, optFns...)
}

func (c ec2Client) DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	return c.deleteSecurityGroup(ctx, in, optFns...)
}
