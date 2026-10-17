package envsourcesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

var pollInterval = 2 * time.Second

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
	steps := []func(context.Context, Clients, placed) error{ensureRoles, ensureLogGroup, ensureFunction, ensureSchedule}
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

func document(statements ...map[string]any) (string, error) {
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	return string(encoded), err
}

func ensureRoles(ctx context.Context, c Clients, at placed) error {
	syncTrust, err := document(map[string]any{
		"Effect": "Allow", "Principal": map[string]any{"Service": lambdaPrincipal}, "Action": "sts:AssumeRole",
	})
	if err != nil {
		return err
	}
	syncPolicy, err := document(
		map[string]any{"Effect": "Allow", "Action": []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query"}, "Resource": at.spec.TableARN},
		map[string]any{"Effect": "Allow", "Action": []string{"kms:Decrypt", "kms:Encrypt"}, "Resource": at.spec.Key},
		map[string]any{"Effect": "Allow", "Action": []string{"logs:CreateLogStream", "logs:PutLogEvents"}, "Resource": at.arn("logs", "log-group:"+at.names.LogGroup+":*")},
	)
	if err != nil {
		return err
	}
	behind := ensureRole(ctx, c, at, at.names.Role, "Ocel: the role the "+string(at.spec.Tier)+" env source sync runs as", syncTrust, syncPolicyName, syncPolicy)
	if behind != nil && !errors.Is(behind, stackrecords.ErrPartBehind) {
		return behind
	}
	scheduleTrust, err := document(map[string]any{
		"Effect": "Allow", "Principal": map[string]any{"Service": schedulerPrincipal}, "Action": "sts:AssumeRole",
		"Condition": map[string]any{
			"StringEquals": map[string]string{"aws:SourceAccount": at.account},
			"ArnEquals":    map[string]string{"aws:SourceArn": at.groupARN()},
		},
	})
	if err != nil {
		return err
	}
	invokePolicy, err := document(map[string]any{"Effect": "Allow", "Action": "lambda:InvokeFunction", "Resource": at.functionARN()})
	if err != nil {
		return err
	}
	err = ensureRole(ctx, c, at, at.names.ScheduleRole, "Ocel: the role EventBridge Scheduler invokes the "+string(at.spec.Tier)+" env source sync through, and nothing else", scheduleTrust, invokePolicyName, invokePolicy)
	if err != nil && !errors.Is(err, stackrecords.ErrPartBehind) {
		return err
	}
	return errors.Join(behind, err)
}

func ensureRole(ctx context.Context, c Clients, at placed, name, description, trust, policyName, policy string) error {
	behind := createRole(ctx, c, at, name, description, trust)
	if behind != nil && !errors.Is(behind, stackrecords.ErrPartBehind) {
		return behind
	}
	if _, err := c.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(name),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policy),
	}); err != nil {
		return fmt.Errorf("put policy %s on role %s: %w", policyName, name, err)
	}
	return behind
}

func createRole(ctx context.Context, c Clients, at placed, name, description, trust string) error {
	in := &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		Path:                     aws.String(at.spec.RolePath),
		Description:              aws.String(description),
		AssumeRolePolicyDocument: aws.String(trust),
		PermissionsBoundary:      aws.String(at.spec.Boundary),
		Tags:                     iamTags(at),
	}
	_, err := c.IAM.CreateRole(ctx, in)
	var exists *iamtypes.EntityAlreadyExistsException
	if err == nil || !errors.As(err, &exists) {
		return wrapped("create role "+name, err)
	}
	current, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if err != nil {
		return fmt.Errorf("read role %s: %w", name, err)
	}
	if boundary := current.Role.PermissionsBoundary; boundary == nil || aws.ToString(boundary.PermissionsBoundaryArn) != at.spec.Boundary {
		if _, err := c.IAM.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{
			RoleName:            aws.String(name),
			PermissionsBoundary: aws.String(at.spec.Boundary),
		}); err != nil {
			return fmt.Errorf("bound role %s by %s: %w", name, at.spec.Boundary, err)
		}
	}
	trusted, err := sameDocument(aws.ToString(current.Role.AssumeRolePolicyDocument), trust)
	if err != nil {
		return fmt.Errorf("read the trust policy of role %s: %w", name, err)
	}
	switch {
	case trusted:
		return nil
	case at.spec.UpdatesTrust:
		if _, err := c.IAM.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(name),
			PolicyDocument: aws.String(trust),
		}); err != nil {
			return fmt.Errorf("put back the trust policy of role %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("role %s trusts other than this build gives it, and a deploy never changes a role's trust: run `%s` to put it back: %w",
		name, provider.BootstrapCommand(at.spec.Tier), stackrecords.ErrPartBehind)
}

func sameDocument(recorded, want string) (bool, error) {
	decoded, err := url.QueryUnescape(recorded)
	if err != nil {
		return false, err
	}
	var got, wanted any
	if err := json.Unmarshal([]byte(decoded), &got); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(want), &wanted); err != nil {
		return false, err
	}
	return reflect.DeepEqual(got, wanted), nil
}

func tags(at placed) map[string]string {
	return map[string]string{managedByTagKey: managedByTagValue, tierTagKey: string(at.spec.Tier)}
}

func iamTags(at placed) []iamtypes.Tag {
	return []iamtypes.Tag{
		{Key: aws.String(managedByTagKey), Value: aws.String(managedByTagValue)},
		{Key: aws.String(tierTagKey), Value: aws.String(string(at.spec.Tier))},
	}
}

func ensureLogGroup(ctx context.Context, c Clients, at placed) error {
	_, err := c.Logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(at.names.LogGroup), Tags: tags(at)})
	var exists *logstypes.ResourceAlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return fmt.Errorf("create log group %s: %w", at.names.LogGroup, err)
	}
	if _, err := c.Logs.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(at.names.LogGroup),
		RetentionInDays: aws.Int32(retentionDays),
	}); err != nil {
		return fmt.Errorf("bound the retention of log group %s: %w", at.names.LogGroup, err)
	}
	return nil
}

func ensureFunction(ctx context.Context, c Clients, at placed) error {
	code := payloads.EnvSourceSync().Bytes
	variables := &lambdatypes.Environment{Variables: map[string]string{
		awsports.VariablesTableEnvVar: at.spec.Table,
		awsports.VariablesKeyEnvVar:   at.spec.Key,
		awsports.TierEnvVar:           string(at.spec.Tier),
	}}
	logging := &lambdatypes.LoggingConfig{LogGroup: aws.String(at.names.LogGroup)}
	role := aws.String(at.roleARN(at.names.Role))
	description := aws.String("Ocel env source sync - reads each scheduled env source registered in the " + string(at.spec.Tier) + " tier and writes what changed into its variables table.")
	exists := false
	err := retrying(ctx, unassumable, func() error {
		_, err := c.Lambda.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName:  aws.String(at.names.Function),
			Description:   description,
			Runtime:       lambdatypes.Runtime(runtime),
			Architectures: []lambdatypes.Architecture{lambdatypes.Architecture(architecture)},
			Handler:       aws.String(handler),
			MemorySize:    aws.Int32(memoryMB),
			Timeout:       aws.Int32(timeoutSeconds),
			Role:          role,
			Code:          &lambdatypes.FunctionCode{ZipFile: code},
			Environment:   variables,
			LoggingConfig: logging,
			Tags:          tags(at),
		})
		var conflict *lambdatypes.ResourceConflictException
		if errors.As(err, &conflict) {
			exists = true
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("create function %s: %w", at.names.Function, err)
	}
	if exists {
		if err := retrying(ctx, unassumableOrUpdating, func() error {
			_, err := c.Lambda.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
				FunctionName:  aws.String(at.names.Function),
				Description:   description,
				Runtime:       lambdatypes.Runtime(runtime),
				Handler:       aws.String(handler),
				MemorySize:    aws.Int32(memoryMB),
				Timeout:       aws.Int32(timeoutSeconds),
				Role:          role,
				Environment:   variables,
				LoggingConfig: logging,
			})
			return err
		}); err != nil {
			return fmt.Errorf("update the configuration of function %s: %w", at.names.Function, err)
		}
		if err := retrying(ctx, updating, func() error {
			_, err := c.Lambda.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
				FunctionName:  aws.String(at.names.Function),
				Architectures: []lambdatypes.Architecture{lambdatypes.Architecture(architecture)},
				ZipFile:       code,
			})
			return err
		}); err != nil {
			return fmt.Errorf("update the code of function %s: %w", at.names.Function, err)
		}
	}
	if err := retrying(ctx, updating, func() error {
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
	return retrying(ctx, unassumable, func() error {
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

func unassumable(err error) bool {
	var invalid *lambdatypes.InvalidParameterValueException
	var validation *schedulertypes.ValidationException
	return (errors.As(err, &invalid) || errors.As(err, &validation)) && strings.Contains(strings.ToLower(err.Error()), "assume")
}

func updating(err error) bool {
	var conflict *lambdatypes.ResourceConflictException
	return errors.As(err, &conflict)
}

func unassumableOrUpdating(err error) bool { return unassumable(err) || updating(err) }

func retrying(ctx context.Context, retryable func(error) bool, call func() error) error {
	for attempt := 0; ; attempt++ {
		err := call()
		if err == nil || !retryable(err) || attempt+1 >= maxRoleAttempts {
			return err
		}
		if err := pause(ctx, attempt); err != nil {
			return err
		}
	}
}

func pause(ctx context.Context, attempt int) error {
	delay := min(pollInterval<<min(attempt, 8), pollCeiling)
	delay += time.Duration(float64(delay) * 0.5 * (2*rand.Float64() - 1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
