package resources

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

const (
	ownCredential      = "its own credential"
	functionCredential = "compute/function"
	networkCredential  = "component network"
	topicCredential    = "resources/topic"
)

type creation struct {
	action    string
	grantedBy string
}

var createActions = map[string]creation{
	"aws:rds/cluster:Cluster":                                {"rds:CreateDBCluster", ownCredential},
	"aws:rds/clusterInstance:ClusterInstance":                {"rds:CreateDBInstance", ownCredential},
	"aws:rds/subnetGroup:SubnetGroup":                        {"rds:CreateDBSubnetGroup", ownCredential},
	"ocel:index:PostgresRole":                                {"rds-data:ExecuteStatement", ownCredential},
	"aws:s3/bucketV2:BucketV2":                               {"s3:CreateBucket", ownCredential},
	"aws:s3/bucketNotification:BucketNotification":           {"s3:PutBucketNotification", ownCredential},
	"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock": {"s3:PutBucketPublicAccessBlock", ownCredential},
	"aws:elasticache/replicationGroup:ReplicationGroup":      {"elasticache:CreateReplicationGroup", ownCredential},
	"aws:elasticache/parameterGroup:ParameterGroup":          {"elasticache:CreateCacheParameterGroup", ownCredential},
	"aws:elasticache/subnetGroup:SubnetGroup":                {"elasticache:CreateCacheSubnetGroup", ownCredential},
	"aws:scheduler/scheduleGroup:ScheduleGroup":              {"scheduler:CreateScheduleGroup", ownCredential},
	"aws:appsync/api:Api":                                    {"appsync:CreateApi", ownCredential},
	"aws:appsync/channelNamespace:ChannelNamespace":          {"appsync:CreateChannelNamespace", ownCredential},
	"aws:sqs/queue:Queue":                                    {"sqs:CreateQueue", topicCredential},
	"aws:sqs/queuePolicy:QueuePolicy":                        {"sqs:SetQueueAttributes", topicCredential},
	"aws:sns/topic:Topic":                                    {"sns:CreateTopic", topicCredential},
	"aws:sns/topicSubscription:TopicSubscription":            {"sns:Subscribe", topicCredential},
	"aws:ec2/securityGroup:SecurityGroup":                    {"ec2:CreateSecurityGroup", networkCredential},
	"aws:lambda/function:Function":                           {"lambda:CreateFunction", functionCredential},
	"aws:lambda/permission:Permission":                       {"lambda:AddPermission", functionCredential},
	"aws:iam/role:Role":                                      {"iam:CreateRole", functionCredential},
	"aws:iam/rolePolicy:RolePolicy":                          {"iam:PutRolePolicy", functionCredential},
	"aws:iam/rolePolicyAttachment:RolePolicyAttachment":      {"iam:AttachRolePolicy", functionCredential},
	"aws:cloudwatch/logGroup:LogGroup":                       {"logs:CreateLogGroup", functionCredential},
}

type absentObjects struct{}

func (absentObjects) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, &s3types.NotFound{}
}

func (absentObjects) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}

type absentParameters struct{ kv.ParametersAPI }

func (absentParameters) GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	return nil, &ssmtypes.ParameterNotFound{}
}

func (absentParameters) PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	return &ssm.PutParameterOutput{}, nil
}

type absentSigningKeys struct{ realtime.SigningKeysAPI }

func (absentSigningKeys) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	return nil, &smtypes.ResourceNotFoundException{}
}

func (absentSigningKeys) CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	return &secretsmanager.CreateSecretOutput{}, nil
}

func masterUserSecret(args pulumi.MockResourceArgs) resource.PropertyMap {
	if args.TypeToken != component.TokenRDSCluster {
		return nil
	}
	return resource.PropertyMap{
		"masterUserSecrets": resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewObjectProperty(resource.PropertyMap{
				"secretArn": resource.NewStringProperty("arn:aws:secretsmanager:us-east-1:123456789012:secret:db-main"),
			}),
		}),
	}
}

type untransformed struct{}

func (untransformed) TagsFor(string, string) map[string]string { return nil }

func (untransformed) Patches(string, string, string) bool { return false }

func representativeResources() []provider.Resource {
	return []provider.Resource{
		{Name: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}},
		{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{}},
		{Name: "realtime--live", Declared: "live", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}},
		{Name: "topic--orders", Declared: "orders", Type: provider.BindingTopic, Topic: &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "audit-log", Worker: "worker"}}}},
		{Name: "topic--heartbeat", Declared: "heartbeat", Type: provider.BindingTask, Topic: &provider.TopicSpec{Cron: "* * * * *", Consumers: []provider.ConsumerSpec{{Name: "heartbeat", Worker: "worker", Exclusive: true}}}},
	}
}

func representativeStack(resources []provider.Resource) component.Stack {
	return component.Stack{
		Ref:            provider.StackRef{Tier: environment.TierProduction, Project: "shop"},
		Project:        "shop",
		Env:            "prod",
		Region:         "us-east-1",
		StateTable:     "ocel-state",
		StateTableARN:  componenttest.StateTableARN,
		AppBoundaryARN: componenttest.BoundaryARN,
		AppRolePath:    componenttest.AppRolePath,
		Network:        component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"}, Clients: "sg-clients"},
		Transforms:     untransformed{},
		Resources:      resources,
	}
}

func grants(statements []iam.Statement, action string) bool {
	return slices.ContainsFunc(statements, func(statement iam.Statement) bool { return slices.Contains(statement.Actions, action) })
}

func TestEveryResourceAComponentDeclaresIsOneTheDeployCredentialMayCreate(t *testing.T) {
	scope := account.Namespace(provider.DefaultNamespace).Scope(environment.TierProduction)
	granting := map[string][]iam.Statement{
		functionCredential: function.Credential(scope),
		networkCredential:  component.NetworkCredential(scope),
		topicCredential:    topic.New().Credential(scope),
	}
	registry := Registry(Clients{
		Artifacts:   component.Artifacts{Objects: absentObjects{}, Bucket: "ocel-artifacts"},
		KVTokens:    kv.Tokens{Parameters: absentParameters{}, Root: "/ocel/kv"},
		SigningKeys: realtime.Keys{SigningKeys: absentSigningKeys{}, Root: "/ocel/realtime"},
	})
	resources := representativeResources()
	at := representativeStack(resources)
	declaredTokens := map[string]bool{}
	for _, declaring := range registry.Components() {
		kinds := declaring.Types
		declared := declaring.Declared(resources)
		if len(declared) == 0 {
			t.Fatalf("no representative resource of %v, so what it declares goes unchecked", kinds)
		}
		if declaring.Credential == nil {
			t.Fatalf("the %v component grants no credential", kinds)
		}
		own := declaring.Credential(scope)
		rec := componenttest.Record(t, func(ctx *pulumi.Context) error { return declaring.Declare(ctx, at, declared) }, masterUserSecret)
		tokens := rec.Tokens()
		if len(tokens) == 0 {
			t.Fatalf("the %v component declared nothing for %v", kinds, declared)
		}
		for _, token := range tokens {
			declaredTokens[token] = true
			created, listed := createActions[token]
			if !listed {
				t.Errorf("the %v component declares %s, which names no IAM create action here: add it, and the grant to the credential that owns it", kinds, token)
				continue
			}
			statements := own
			if created.grantedBy != ownCredential {
				statements = granting[created.grantedBy]
			}
			if !grants(statements, created.action) {
				t.Errorf("the %v component declares %s, and %s does not grant %s, so a deploy is refused creating it", kinds, token, created.grantedBy, created.action)
			}
		}
	}
	for token := range createActions {
		if !declaredTokens[token] {
			t.Errorf("%s is listed with the action that creates it, and no component declares it any more", token)
		}
	}
}
