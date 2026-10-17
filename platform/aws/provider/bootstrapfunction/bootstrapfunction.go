package bootstrapfunction

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
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var pollInterval = 2 * time.Second

const (
	pollCeiling = 10 * time.Second
	maxAttempts = 12
)

type Role struct {
	Name         string
	Path         string
	Boundary     string
	Description  string
	Service      string
	Condition    map[string]any
	UpdatesTrust bool
	PolicyName   string
	Policy       string
	Tags         map[string]string
}

type Function struct {
	Name           string
	Description    string
	Runtime        string
	Architecture   string
	Handler        string
	MemoryMB       int32
	TimeoutSeconds int32
	Role           string
	Code           []byte
	Variables      map[string]string
	LogGroup       string
	RetentionDays  int32
	Tags           map[string]string
}

func Document(statements ...map[string]any) (string, error) {
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	return string(encoded), err
}

func EnsureRole(ctx context.Context, c IAMAPI, role Role) error {
	statement := map[string]any{
		"Effect": "Allow", "Principal": map[string]any{"Service": role.Service}, "Action": "sts:AssumeRole",
	}
	if role.Condition != nil {
		statement["Condition"] = role.Condition
	}
	trust, err := Document(statement)
	if err != nil {
		return err
	}
	behind := createRole(ctx, c, role, trust)
	if behind != nil && !errors.Is(behind, stackrecords.ErrPartBehind) {
		return behind
	}
	if _, err := c.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role.Name),
		PolicyName:     aws.String(role.PolicyName),
		PolicyDocument: aws.String(role.Policy),
	}); err != nil {
		return fmt.Errorf("put policy %s on role %s: %w", role.PolicyName, role.Name, err)
	}
	return behind
}

func createRole(ctx context.Context, c IAMAPI, role Role, trust string) error {
	in := &iam.CreateRoleInput{
		RoleName:                 aws.String(role.Name),
		Path:                     aws.String(role.Path),
		Description:              aws.String(role.Description),
		AssumeRolePolicyDocument: aws.String(trust),
		Tags:                     iamTags(role.Tags),
	}
	if role.Boundary != "" {
		in.PermissionsBoundary = aws.String(role.Boundary)
	}
	_, err := c.CreateRole(ctx, in)
	var exists *iamtypes.EntityAlreadyExistsException
	if err == nil || !errors.As(err, &exists) {
		return wrapped("create role "+role.Name, err)
	}
	current, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(role.Name)})
	if err != nil {
		return fmt.Errorf("read role %s: %w", role.Name, err)
	}
	if role.Boundary != "" && boundaryOf(current.Role) != role.Boundary {
		if _, err := c.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{
			RoleName:            aws.String(role.Name),
			PermissionsBoundary: aws.String(role.Boundary),
		}); err != nil {
			return fmt.Errorf("bound role %s by %s: %w", role.Name, role.Boundary, err)
		}
	}
	trusted, err := sameDocument(aws.ToString(current.Role.AssumeRolePolicyDocument), trust)
	if err != nil {
		return fmt.Errorf("read the trust policy of role %s: %w", role.Name, err)
	}
	switch {
	case trusted:
		return nil
	case role.UpdatesTrust:
		if _, err := c.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(role.Name),
			PolicyDocument: aws.String(trust),
		}); err != nil {
			return fmt.Errorf("put back the trust policy of role %s: %w", role.Name, err)
		}
		return nil
	}
	return fmt.Errorf("role %s trusts other than this build gives it, and only a bootstrap changes a role's trust: %w", role.Name, stackrecords.ErrPartBehind)
}

func boundaryOf(role *iamtypes.Role) string {
	if role == nil || role.PermissionsBoundary == nil {
		return ""
	}
	return aws.ToString(role.PermissionsBoundary.PermissionsBoundaryArn)
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

func wrapped(doing string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", doing, err)
}

func iamTags(tags map[string]string) []iamtypes.Tag {
	out := make([]iamtypes.Tag, 0, len(tags))
	for key, value := range tags {
		out = append(out, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

func Ensure(ctx context.Context, c Clients, fn Function) error {
	if err := ensureLogGroup(ctx, c.Logs, fn); err != nil {
		return err
	}
	return ensureFunction(ctx, c.Lambda, fn)
}

func ensureLogGroup(ctx context.Context, c LogsAPI, fn Function) error {
	_, err := c.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(fn.LogGroup), Tags: fn.Tags})
	var exists *logstypes.ResourceAlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return fmt.Errorf("create log group %s: %w", fn.LogGroup, err)
	}
	if _, err := c.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(fn.LogGroup),
		RetentionInDays: aws.Int32(fn.RetentionDays),
	}); err != nil {
		return fmt.Errorf("bound the retention of log group %s: %w", fn.LogGroup, err)
	}
	return nil
}

func ensureFunction(ctx context.Context, c LambdaAPI, fn Function) error {
	variables := &lambdatypes.Environment{Variables: fn.Variables}
	logging := &lambdatypes.LoggingConfig{LogGroup: aws.String(fn.LogGroup)}
	exists := false
	err := Retrying(ctx, unassumable, func() error {
		_, err := c.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName:  aws.String(fn.Name),
			Description:   aws.String(fn.Description),
			Runtime:       lambdatypes.Runtime(fn.Runtime),
			Architectures: []lambdatypes.Architecture{lambdatypes.Architecture(fn.Architecture)},
			Handler:       aws.String(fn.Handler),
			MemorySize:    aws.Int32(fn.MemoryMB),
			Timeout:       aws.Int32(fn.TimeoutSeconds),
			Role:          aws.String(fn.Role),
			Code:          &lambdatypes.FunctionCode{ZipFile: fn.Code},
			Environment:   variables,
			LoggingConfig: logging,
			Tags:          fn.Tags,
		})
		var conflict *lambdatypes.ResourceConflictException
		if errors.As(err, &conflict) {
			exists = true
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("create function %s: %w", fn.Name, err)
	}
	if !exists {
		return nil
	}
	if err := Retrying(ctx, unassumableOrUpdating, func() error {
		_, err := c.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
			FunctionName:  aws.String(fn.Name),
			Description:   aws.String(fn.Description),
			Runtime:       lambdatypes.Runtime(fn.Runtime),
			Handler:       aws.String(fn.Handler),
			MemorySize:    aws.Int32(fn.MemoryMB),
			Timeout:       aws.Int32(fn.TimeoutSeconds),
			Role:          aws.String(fn.Role),
			Environment:   variables,
			LoggingConfig: logging,
		})
		return err
	}); err != nil {
		return fmt.Errorf("update the configuration of function %s: %w", fn.Name, err)
	}
	if err := Retrying(ctx, Updating, func() error {
		_, err := c.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
			FunctionName:  aws.String(fn.Name),
			Architectures: []lambdatypes.Architecture{lambdatypes.Architecture(fn.Architecture)},
			ZipFile:       fn.Code,
		})
		return err
	}); err != nil {
		return fmt.Errorf("update the code of function %s: %w", fn.Name, err)
	}
	return nil
}

func Remove(ctx context.Context, c Clients, function, logGroup string) error {
	var missingFunction *lambdatypes.ResourceNotFoundException
	if _, err := c.Lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(function)}); err != nil && !errors.As(err, &missingFunction) {
		return fmt.Errorf("delete function %s: %w", function, err)
	}
	var missingGroup *logstypes.ResourceNotFoundException
	if _, err := c.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)}); err != nil && !errors.As(err, &missingGroup) {
		return fmt.Errorf("delete log group %s: %w", logGroup, err)
	}
	return nil
}

func RemoveRole(ctx context.Context, c IAMAPI, name string) error {
	var missing *iamtypes.NoSuchEntityException
	policies, err := c.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(name)})
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list the policies of role %s: %w", name, err)
	}
	for _, policy := range policies.PolicyNames {
		if _, err := c.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(policy)}); err != nil && !errors.As(err, &missing) {
			return fmt.Errorf("delete policy %s of role %s: %w", policy, name, err)
		}
	}
	if _, err := c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}); err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete role %s: %w", name, err)
	}
	return nil
}

func unassumable(err error) bool {
	var invalid *lambdatypes.InvalidParameterValueException
	return errors.As(err, &invalid) && strings.Contains(strings.ToLower(err.Error()), "assume")
}

func Updating(err error) bool {
	var conflict *lambdatypes.ResourceConflictException
	var throttled *lambdatypes.TooManyRequestsException
	return errors.As(err, &conflict) || errors.As(err, &throttled)
}

func unassumableOrUpdating(err error) bool { return unassumable(err) || Updating(err) }

func Retrying(ctx context.Context, retryable func(error) bool, call func() error) error {
	for attempt := 0; ; attempt++ {
		err := call()
		if err == nil || !retryable(err) || attempt+1 >= maxAttempts {
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
