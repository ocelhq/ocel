package envsourcesync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrapfunction"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type placed struct {
	spec      Spec
	names     Names
	partition string
	account   string
}

func (p placed) arn(service, resource string) string {
	return "arn:" + p.partition + ":" + service + ":" + p.spec.Region + ":" + p.account + ":" + resource
}

func (p placed) roleARN(name string) string {
	return "arn:" + p.partition + ":iam::" + p.account + ":role" + p.spec.RolePath + name
}

func (p placed) functionARN() string { return p.arn("lambda", "function:"+p.names.Function) }

func (p placed) groupARN() string { return p.arn("scheduler", "schedule-group/"+p.names.ScheduleGroup) }

func Ensure(ctx context.Context, c Clients, spec Spec) ([]provider.PartResource, error) {
	if spec.Key == "" {
		return nil, awsports.RefuseUnkeyed(spec.Tier)
	}
	boundary, err := arn.Parse(spec.Boundary)
	if err != nil {
		return nil, fmt.Errorf("the app boundary %q is no ARN, so the env source sync's roles cannot name the account they trust: %w", spec.Boundary, err)
	}
	at := placed{spec: spec, names: NamesFor(spec.Namespace, spec.Tier), partition: boundary.Partition, account: boundary.AccountID}
	steps := []func(context.Context, Clients, placed) error{ensureRoles, ensureFunction, ensureSchedule}
	var behind error
	for _, step := range steps {
		err := step(ctx, c, at)
		if errors.Is(err, stackrecords.ErrPartBehind) {
			behind = err
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return Resources(spec.Namespace, spec.Tier), behind
}

func Resources(namespace string, tier environment.Tier) []provider.PartResource {
	names := NamesFor(namespace, tier)
	return []provider.PartResource{
		{Kind: "AWS::IAM::Role", ID: names.Role},
		{Kind: "AWS::IAM::Role", ID: names.ScheduleRole},
		{Kind: "AWS::Logs::LogGroup", ID: names.LogGroup},
		{Kind: "AWS::Lambda::Function", ID: names.Function},
		{Kind: "AWS::Scheduler::ScheduleGroup", ID: names.ScheduleGroup},
		{Kind: "AWS::Scheduler::Schedule", ID: names.Schedule},
	}
}

func ensureRoles(ctx context.Context, c Clients, at placed) error {
	syncPolicy, err := bootstrapfunction.Document(
		map[string]any{"Effect": "Allow", "Action": []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query"}, "Resource": at.spec.TableARN},
		map[string]any{"Effect": "Allow", "Action": []string{"kms:Decrypt", "kms:Encrypt"}, "Resource": at.spec.Key},
		map[string]any{"Effect": "Allow", "Action": []string{"logs:CreateLogStream", "logs:PutLogEvents"}, "Resource": at.arn("logs", "log-group:"+at.names.LogGroup+":*")},
	)
	if err != nil {
		return err
	}
	behind := bootstrapfunction.EnsureRole(ctx, c.IAM, bootstrapfunction.Role{
		Name:         at.names.Role,
		Path:         at.spec.RolePath,
		Boundary:     at.spec.Boundary,
		Description:  "Ocel: the role the " + string(at.spec.Tier) + " env source sync runs as",
		Service:      lambdaPrincipal,
		PolicyName:   syncPolicyName,
		Policy:       syncPolicy,
		Tags:         tags(at),
		UpdatesTrust: at.spec.UpdatesTrust,
	})
	if behind != nil && !errors.Is(behind, stackrecords.ErrPartBehind) {
		return behind
	}
	invokePolicy, err := bootstrapfunction.Document(map[string]any{"Effect": "Allow", "Action": "lambda:InvokeFunction", "Resource": at.functionARN()})
	if err != nil {
		return err
	}
	err = bootstrapfunction.EnsureRole(ctx, c.IAM, bootstrapfunction.Role{
		Name:        at.names.ScheduleRole,
		Path:        at.spec.RolePath,
		Boundary:    at.spec.Boundary,
		Description: "Ocel: the role EventBridge Scheduler invokes the " + string(at.spec.Tier) + " env source sync through, and nothing else",
		Service:     schedulerPrincipal,
		Condition: map[string]any{
			"StringEquals": map[string]string{"aws:SourceAccount": at.account},
			"ArnEquals":    map[string]string{"aws:SourceArn": at.groupARN()},
		},
		PolicyName:   invokePolicyName,
		Policy:       invokePolicy,
		Tags:         tags(at),
		UpdatesTrust: at.spec.UpdatesTrust,
	})
	if err != nil && !errors.Is(err, stackrecords.ErrPartBehind) {
		return err
	}
	return errors.Join(behind, err)
}

func tags(at placed) map[string]string {
	return map[string]string{managedByTagKey: managedByTagValue, tierTagKey: string(at.spec.Tier)}
}

func ensureFunction(ctx context.Context, c Clients, at placed) error {
	if err := bootstrapfunction.Ensure(ctx, c.functions(), bootstrapfunction.Function{
		Name:           at.names.Function,
		Description:    "Ocel env source sync - reads each scheduled env source registered in the " + string(at.spec.Tier) + " tier and writes what changed into its variables table.",
		Runtime:        runtime,
		Architecture:   architecture,
		Handler:        handler,
		MemoryMB:       memoryMB,
		TimeoutSeconds: timeoutSeconds,
		Role:           at.roleARN(at.names.Role),
		Code:           payloads.EnvSourceSync().Bytes,
		Variables: map[string]string{
			awsports.VariablesTableEnvVar: at.spec.Table,
			awsports.VariablesKeyEnvVar:   at.spec.Key,
			awsports.TierEnvVar:           string(at.spec.Tier),
		},
		LogGroup:      at.names.LogGroup,
		RetentionDays: retentionDays,
		Tags:          tags(at),
	}); err != nil {
		return err
	}
	if err := bootstrapfunction.Retrying(ctx, bootstrapfunction.Updating, func() error {
		_, err := c.Lambda.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
			FunctionName:             aws.String(at.names.Function),
			Qualifier:                aws.String("$LATEST"),
			MaximumRetryAttempts:     aws.Int32(0),
			MaximumEventAgeInSeconds: aws.Int32(maxEventSeconds),
		})
		return err
	}); err != nil {
		return fmt.Errorf("stop function %s retrying a failed or throttled sync: %w", at.names.Function, err)
	}
	return nil
}

func ensureSchedule(ctx context.Context, c Clients, at placed) error {
	_, err := c.Scheduler.CreateScheduleGroup(ctx, &scheduler.CreateScheduleGroupInput{
		Name: aws.String(at.names.ScheduleGroup),
		Tags: []schedulertypes.Tag{
			{Key: aws.String(managedByTagKey), Value: aws.String(managedByTagValue)},
			{Key: aws.String(tierTagKey), Value: aws.String(string(at.spec.Tier))},
		},
	})
	var conflict *schedulertypes.ConflictException
	if err != nil && !errors.As(err, &conflict) {
		return fmt.Errorf("create schedule group %s: %w", at.names.ScheduleGroup, err)
	}
	target := &schedulertypes.Target{
		Arn:         aws.String(at.functionARN()),
		RoleArn:     aws.String(at.roleARN(at.names.ScheduleRole)),
		RetryPolicy: &schedulertypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(0)},
	}
	description := aws.String("Runs the " + string(at.spec.Tier) + " env source sync once a minute. Each env source waits out its own backoff, so a failing one is not read every minute.")
	window := &schedulertypes.FlexibleTimeWindow{Mode: schedulertypes.FlexibleTimeWindowModeOff}
	return bootstrapfunction.Retrying(ctx, scheduleUnassumable, func() error {
		_, err := c.Scheduler.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name:               aws.String(at.names.Schedule),
			GroupName:          aws.String(at.names.ScheduleGroup),
			Description:        description,
			ScheduleExpression: aws.String(rate),
			FlexibleTimeWindow: window,
			State:              schedulertypes.ScheduleStateEnabled,
			Target:             target,
		})
		if !errors.As(err, &conflict) {
			return wrapped("create schedule "+at.names.Schedule, err)
		}
		_, err = c.Scheduler.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
			Name:               aws.String(at.names.Schedule),
			GroupName:          aws.String(at.names.ScheduleGroup),
			Description:        description,
			ScheduleExpression: aws.String(rate),
			FlexibleTimeWindow: window,
			State:              schedulertypes.ScheduleStateEnabled,
			Target:             target,
		})
		return wrapped("update schedule "+at.names.Schedule, err)
	})
}

func wrapped(doing string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", doing, err)
}

func scheduleUnassumable(err error) bool {
	var validation *schedulertypes.ValidationException
	return errors.As(err, &validation) && strings.Contains(strings.ToLower(err.Error()), "assume")
}
