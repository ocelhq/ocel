package envsourcesync

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrapfunction"
)

type LambdaAPI interface {
	bootstrapfunction.LambdaAPI
	PutFunctionEventInvokeConfig(ctx context.Context, in *lambda.PutFunctionEventInvokeConfigInput, optFns ...func(*lambda.Options)) (*lambda.PutFunctionEventInvokeConfigOutput, error)
}

type SchedulerAPI interface {
	CreateScheduleGroup(ctx context.Context, in *scheduler.CreateScheduleGroupInput, optFns ...func(*scheduler.Options)) (*scheduler.CreateScheduleGroupOutput, error)
	DeleteScheduleGroup(ctx context.Context, in *scheduler.DeleteScheduleGroupInput, optFns ...func(*scheduler.Options)) (*scheduler.DeleteScheduleGroupOutput, error)
	CreateSchedule(ctx context.Context, in *scheduler.CreateScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error)
	UpdateSchedule(ctx context.Context, in *scheduler.UpdateScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error)
	DeleteSchedule(ctx context.Context, in *scheduler.DeleteScheduleInput, optFns ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error)
}

type Clients struct {
	IAM       bootstrapfunction.IAMAPI
	Lambda    LambdaAPI
	Logs      bootstrapfunction.LogsAPI
	Scheduler SchedulerAPI
}

func (c Clients) functions() bootstrapfunction.Clients {
	return bootstrapfunction.Clients{IAM: c.IAM, Lambda: c.Lambda, Logs: c.Logs}
}

func NewClients(cfg aws.Config) Clients {
	return Clients{
		IAM:       iam.NewFromConfig(cfg),
		Lambda:    lambda.NewFromConfig(cfg),
		Logs:      cloudwatchlogs.NewFromConfig(cfg),
		Scheduler: scheduler.NewFromConfig(cfg),
	}
}
