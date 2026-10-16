package envsourcesync

import (
	"context"
	"errors"
	"fmt"

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
)

func Remove(ctx context.Context, c Clients, namespace string, tier environment.Tier) error {
	names := NamesFor(namespace, tier)
	var missingSchedule *schedulertypes.ResourceNotFoundException
	if _, err := c.Scheduler.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(names.Schedule), GroupName: aws.String(names.ScheduleGroup)}); err != nil && !errors.As(err, &missingSchedule) {
		return fmt.Errorf("delete schedule %s: %w", names.Schedule, err)
	}
	if _, err := c.Scheduler.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: aws.String(names.ScheduleGroup)}); err != nil && !errors.As(err, &missingSchedule) {
		return fmt.Errorf("delete schedule group %s: %w", names.ScheduleGroup, err)
	}
	var missingFunction *lambdatypes.ResourceNotFoundException
	if _, err := c.Lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(names.Function)}); err != nil && !errors.As(err, &missingFunction) {
		return fmt.Errorf("delete function %s: %w", names.Function, err)
	}
	var missingGroup *logstypes.ResourceNotFoundException
	if _, err := c.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(names.LogGroup)}); err != nil && !errors.As(err, &missingGroup) {
		return fmt.Errorf("delete log group %s: %w", names.LogGroup, err)
	}
	for _, role := range []string{names.ScheduleRole, names.Role} {
		if err := removeRole(ctx, c, role); err != nil {
			return err
		}
	}
	return nil
}

func removeRole(ctx context.Context, c Clients, name string) error {
	var missing *iamtypes.NoSuchEntityException
	policies, err := c.IAM.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(name)})
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list the policies of role %s: %w", name, err)
	}
	for _, policy := range policies.PolicyNames {
		if _, err := c.IAM.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(policy)}); err != nil && !errors.As(err, &missing) {
			return fmt.Errorf("delete policy %s of role %s: %w", policy, name, err)
		}
	}
	if _, err := c.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}); err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete role %s: %w", name, err)
	}
	return nil
}
