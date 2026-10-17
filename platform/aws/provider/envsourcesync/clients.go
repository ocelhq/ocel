package envsourcesync

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
)

type IAMAPI interface {
	CreateRole(ctx context.Context, in *iam.CreateRoleInput, optFns ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	GetRole(ctx context.Context, in *iam.GetRoleInput, optFns ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	PutRolePolicy(ctx context.Context, in *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	UpdateAssumeRolePolicy(ctx context.Context, in *iam.UpdateAssumeRolePolicyInput, optFns ...func(*iam.Options)) (*iam.UpdateAssumeRolePolicyOutput, error)
	PutRolePermissionsBoundary(ctx context.Context, in *iam.PutRolePermissionsBoundaryInput, optFns ...func(*iam.Options)) (*iam.PutRolePermissionsBoundaryOutput, error)
	ListRolePolicies(ctx context.Context, in *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	DeleteRolePolicy(ctx context.Context, in *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	DeleteRole(ctx context.Context, in *iam.DeleteRoleInput, optFns ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

type LambdaAPI interface {
	CreateFunction(ctx context.Context, in *lambda.CreateFunctionInput, optFns ...func(*lambda.Options)) (*lambda.CreateFunctionOutput, error)
	UpdateFunctionCode(ctx context.Context, in *lambda.UpdateFunctionCodeInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	UpdateFunctionConfiguration(ctx context.Context, in *lambda.UpdateFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error)
	PutFunctionEventInvokeConfig(ctx context.Context, in *lambda.PutFunctionEventInvokeConfigInput, optFns ...func(*lambda.Options)) (*lambda.PutFunctionEventInvokeConfigOutput, error)
	DeleteFunction(ctx context.Context, in *lambda.DeleteFunctionInput, optFns ...func(*lambda.Options)) (*lambda.DeleteFunctionOutput, error)
}

type LogsAPI interface {
	CreateLogGroup(ctx context.Context, in *cloudwatchlogs.CreateLogGroupInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	PutRetentionPolicy(ctx context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error)
	DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
}

type SchedulerAPI interface {
	CreateScheduleGroup(ctx context.Context, in *scheduler.CreateScheduleGroupInput, optFns ...func(*scheduler.Options)) (*scheduler.CreateScheduleGroupOutput, error)
	DeleteScheduleGroup(ctx context.Context, in *scheduler.DeleteScheduleGroupInput, optFns ...func(*scheduler.Options)) (*scheduler.DeleteScheduleGroupOutput, error)
	CreateSchedule(ctx context.Context, in *scheduler.CreateScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error)
	UpdateSchedule(ctx context.Context, in *scheduler.UpdateScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error)
	DeleteSchedule(ctx context.Context, in *scheduler.DeleteScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error)
}

type Clients struct {
	IAM       IAMAPI
	Lambda    LambdaAPI
	Logs      LogsAPI
	Scheduler SchedulerAPI
}

func NewClients(cfg aws.Config) Clients {
	return Clients{
		IAM:       iam.NewFromConfig(cfg),
		Lambda:    lambda.NewFromConfig(cfg),
		Logs:      cloudwatchlogs.NewFromConfig(cfg),
		Scheduler: scheduler.NewFromConfig(cfg),
	}
}
