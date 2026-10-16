package control

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
)

type absentEnvSync struct {
	envsourcesync.IAMAPI
	envsourcesync.LambdaAPI
	envsourcesync.LogsAPI
	envsourcesync.SchedulerAPI

	deleted []string
}

func (a *absentEnvSync) clients() envsourcesync.Clients {
	return envsourcesync.Clients{IAM: a, Lambda: a, Logs: a, Scheduler: a}
}

func (a *absentEnvSync) DeleteSchedule(_ context.Context, in *scheduler.DeleteScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.Name))
	return nil, &schedulertypes.ResourceNotFoundException{}
}

func (a *absentEnvSync) DeleteScheduleGroup(_ context.Context, in *scheduler.DeleteScheduleGroupInput, _ ...func(*scheduler.Options)) (*scheduler.DeleteScheduleGroupOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.Name))
	return nil, &schedulertypes.ResourceNotFoundException{}
}

func (a *absentEnvSync) DeleteFunction(_ context.Context, in *lambda.DeleteFunctionInput, _ ...func(*lambda.Options)) (*lambda.DeleteFunctionOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.FunctionName))
	return nil, &lambdatypes.ResourceNotFoundException{}
}

func (a *absentEnvSync) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.LogGroupName))
	return nil, &logstypes.ResourceNotFoundException{}
}

func (a *absentEnvSync) ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	return nil, &iamtypes.NoSuchEntityException{}
}

func TestRemoveTakesDownTheTiersEnvSourceSync(t *testing.T) {
	t.Parallel()

	b := installedBootstrapper(t, environment.TierPreview)
	sync := &absentEnvSync{}
	b.EnvSync = sync.clients()

	if err := b.Remove(context.Background(), environment.TierPreview, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	names := envsourcesync.NamesFor(string(defaultNamespace), environment.TierPreview)
	for _, name := range []string{names.Schedule, names.ScheduleGroup, names.Function, names.LogGroup} {
		if !slices.Contains(sync.deleted, name) {
			t.Errorf("Remove() deleted %v, want %s of the preview env source sync among them", sync.deleted, name)
		}
	}
}
