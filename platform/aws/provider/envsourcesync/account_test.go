package envsourcesync_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
)

const testAccount = "123456789012"

type role struct {
	id       int
	path     string
	boundary string
	trust    string
	tags     map[string]string
	policies map[string]string
}

type function struct {
	input       lambda.CreateFunctionInput
	roleID      int
	code        []byte
	invokeRetry *int32
	invokeAge   *int32
	tags        map[string]string
}

type schedule struct {
	input scheduler.CreateScheduleInput
}

type account struct {
	mu sync.Mutex

	roles     map[string]*role
	functions map[string]*function
	logGroups map[string]map[string]string
	retention map[string]int32
	groups    map[string]map[string]string
	schedules map[string]*schedule

	unassumable int
	roleIDs     int
	throttled   int
	calls       []string
}

func newAccount() *account {
	return &account{
		roles:     map[string]*role{},
		functions: map[string]*function{},
		logGroups: map[string]map[string]string{},
		retention: map[string]int32{},
		groups:    map[string]map[string]string{},
		schedules: map[string]*schedule{},
	}
}

func (a *account) clients() envsourcesync.Clients {
	return envsourcesync.Clients{IAM: iamAPI{a}, Lambda: lambdaAPI{a}, Logs: logsAPI{a}, Scheduler: schedulerAPI{a}}
}

func (a *account) record(call string) {
	a.calls = append(a.calls, call)
}

func (a *account) idOfRole(arn string) int {
	if r, ok := a.roles[arn[strings.LastIndex(arn, "/")+1:]]; ok {
		return r.id
	}
	return 0
}

func (a *account) functionRunsAsItsRole(name string) bool {
	f, ok := a.functions[name]
	return ok && f.roleID != 0 && f.roleID == a.idOfRole(aws.ToString(f.input.Role))
}

func iamTags(tags []iamtypes.Tag) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

type iamAPI struct{ *account }

func roleARN(path, name string) string {
	return "arn:aws:iam::" + testAccount + ":role" + path + name
}

func (a iamAPI) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateRole")
	name := aws.ToString(in.RoleName)
	if _, ok := a.roles[name]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}
	}
	a.roleIDs++
	a.roles[name] = &role{id: a.roleIDs, path: aws.ToString(in.Path), boundary: aws.ToString(in.PermissionsBoundary), trust: aws.ToString(in.AssumeRolePolicyDocument), tags: iamTags(in.Tags), policies: map[string]string{}}
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{RoleName: in.RoleName, Arn: aws.String(roleARN(aws.ToString(in.Path), name))}}, nil
}

func (a iamAPI) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRolePolicy")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	r.policies[aws.ToString(in.PolicyName)] = aws.ToString(in.PolicyDocument)
	return &iam.PutRolePolicyOutput{}, nil
}

func (a iamAPI) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("GetRole")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	return &iam.GetRoleOutput{Role: &iamtypes.Role{
		RoleName:                 in.RoleName,
		AssumeRolePolicyDocument: aws.String(url.QueryEscape(r.trust)),
		PermissionsBoundary:      &iamtypes.AttachedPermissionsBoundary{PermissionsBoundaryArn: aws.String(r.boundary)},
	}}, nil
}

func (a iamAPI) UpdateAssumeRolePolicy(_ context.Context, in *iam.UpdateAssumeRolePolicyInput, _ ...func(*iam.Options)) (*iam.UpdateAssumeRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateAssumeRolePolicy")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	if r.boundary != testBoundary {
		return nil, fmt.Errorf("AccessDenied: role %s is not under the app boundary, which iam:UpdateAssumeRolePolicy is granted on", aws.ToString(in.RoleName))
	}
	r.trust = aws.ToString(in.PolicyDocument)
	return &iam.UpdateAssumeRolePolicyOutput{}, nil
}

func (a iamAPI) PutRolePermissionsBoundary(_ context.Context, in *iam.PutRolePermissionsBoundaryInput, _ ...func(*iam.Options)) (*iam.PutRolePermissionsBoundaryOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRolePermissionsBoundary")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	r.boundary = aws.ToString(in.PermissionsBoundary)
	return &iam.PutRolePermissionsBoundaryOutput{}, nil
}

func (a iamAPI) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("ListRolePolicies")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	var names []string
	for name := range r.policies {
		names = append(names, name)
	}
	return &iam.ListRolePoliciesOutput{PolicyNames: names}, nil
}

func (a iamAPI) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteRolePolicy")
	if r, ok := a.roles[aws.ToString(in.RoleName)]; ok {
		delete(r.policies, aws.ToString(in.PolicyName))
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (a iamAPI) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteRole")
	r, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	if len(r.policies) > 0 {
		return nil, &iamtypes.DeleteConflictException{Message: aws.String("role has policies")}
	}
	delete(a.roles, aws.ToString(in.RoleName))
	return &iam.DeleteRoleOutput{}, nil
}

type lambdaAPI struct{ *account }

func functionARN(name string) string {
	return "arn:aws:lambda:us-east-1:" + testAccount + ":function:" + name
}

func (a lambdaAPI) CreateFunction(_ context.Context, in *lambda.CreateFunctionInput, _ ...func(*lambda.Options)) (*lambda.CreateFunctionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateFunction")
	if a.unassumable > 0 {
		a.unassumable--
		return nil, &lambdatypes.InvalidParameterValueException{Message: aws.String("The role defined for the function cannot be assumed by Lambda.")}
	}
	name := aws.ToString(in.FunctionName)
	if _, ok := a.functions[name]; ok {
		return nil, &lambdatypes.ResourceConflictException{Message: aws.String("exists")}
	}
	a.functions[name] = &function{input: *in, roleID: a.idOfRole(aws.ToString(in.Role)), code: in.Code.ZipFile, tags: in.Tags}
	return &lambda.CreateFunctionOutput{FunctionArn: aws.String(functionARN(name))}, nil
}

func (a lambdaAPI) UpdateFunctionCode(_ context.Context, in *lambda.UpdateFunctionCodeInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateFunctionCode")
	f, ok := a.functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	f.code = in.ZipFile
	return &lambda.UpdateFunctionCodeOutput{}, nil
}

func (a lambdaAPI) UpdateFunctionConfiguration(_ context.Context, in *lambda.UpdateFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateFunctionConfiguration")
	f, ok := a.functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	if in.Role != nil && aws.ToString(in.Role) != aws.ToString(f.input.Role) {
		f.roleID = a.idOfRole(aws.ToString(in.Role))
	}
	f.input.Environment, f.input.MemorySize, f.input.Timeout, f.input.Role = in.Environment, in.MemorySize, in.Timeout, in.Role
	return &lambda.UpdateFunctionConfigurationOutput{}, nil
}

func (a lambdaAPI) PutFunctionEventInvokeConfig(_ context.Context, in *lambda.PutFunctionEventInvokeConfigInput, _ ...func(*lambda.Options)) (*lambda.PutFunctionEventInvokeConfigOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutFunctionEventInvokeConfig")
	if a.throttled > 0 {
		a.throttled--
		return nil, &lambdatypes.TooManyRequestsException{Message: aws.String("Rate exceeded")}
	}
	f, ok := a.functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	f.invokeRetry, f.invokeAge = in.MaximumRetryAttempts, in.MaximumEventAgeInSeconds
	return &lambda.PutFunctionEventInvokeConfigOutput{}, nil
}

func (a lambdaAPI) DeleteFunction(_ context.Context, in *lambda.DeleteFunctionInput, _ ...func(*lambda.Options)) (*lambda.DeleteFunctionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteFunction")
	if _, ok := a.functions[aws.ToString(in.FunctionName)]; !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	delete(a.functions, aws.ToString(in.FunctionName))
	return &lambda.DeleteFunctionOutput{}, nil
}

type logsAPI struct{ *account }

func (a logsAPI) CreateLogGroup(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateLogGroup")
	name := aws.ToString(in.LogGroupName)
	if _, ok := a.logGroups[name]; ok {
		return nil, &logstypes.ResourceAlreadyExistsException{Message: aws.String("exists")}
	}
	a.logGroups[name] = in.Tags
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}

func (a logsAPI) PutRetentionPolicy(_ context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRetentionPolicy")
	a.retention[aws.ToString(in.LogGroupName)] = aws.ToInt32(in.RetentionInDays)
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}

func (a logsAPI) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteLogGroup")
	if _, ok := a.logGroups[aws.ToString(in.LogGroupName)]; !ok {
		return nil, &logstypes.ResourceNotFoundException{Message: aws.String("no group")}
	}
	delete(a.logGroups, aws.ToString(in.LogGroupName))
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}

type schedulerAPI struct{ *account }

func scheduleKey(group, name *string) string { return aws.ToString(group) + "/" + aws.ToString(name) }

func (a schedulerAPI) CreateScheduleGroup(_ context.Context, in *scheduler.CreateScheduleGroupInput, _ ...func(*scheduler.Options)) (*scheduler.CreateScheduleGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateScheduleGroup")
	name := aws.ToString(in.Name)
	if _, ok := a.groups[name]; ok {
		return nil, &schedulertypes.ConflictException{Message: aws.String("exists")}
	}
	tags := map[string]string{}
	for _, tag := range in.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	a.groups[name] = tags
	return &scheduler.CreateScheduleGroupOutput{}, nil
}

func (a schedulerAPI) DeleteScheduleGroup(_ context.Context, in *scheduler.DeleteScheduleGroupInput, _ ...func(*scheduler.Options)) (*scheduler.DeleteScheduleGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteScheduleGroup")
	name := aws.ToString(in.Name)
	if _, ok := a.groups[name]; !ok {
		return nil, &schedulertypes.ResourceNotFoundException{Message: aws.String("no group")}
	}
	for key := range a.schedules {
		if strings.HasPrefix(key, name+"/") {
			delete(a.schedules, key)
		}
	}
	delete(a.groups, name)
	return &scheduler.DeleteScheduleGroupOutput{}, nil
}

func (a schedulerAPI) CreateSchedule(_ context.Context, in *scheduler.CreateScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateSchedule")
	if _, ok := a.groups[aws.ToString(in.GroupName)]; !ok {
		return nil, &schedulertypes.ResourceNotFoundException{Message: aws.String("no group")}
	}
	key := scheduleKey(in.GroupName, in.Name)
	if _, ok := a.schedules[key]; ok {
		return nil, &schedulertypes.ConflictException{Message: aws.String("exists")}
	}
	a.schedules[key] = &schedule{input: *in}
	return &scheduler.CreateScheduleOutput{}, nil
}

func (a schedulerAPI) UpdateSchedule(_ context.Context, in *scheduler.UpdateScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateSchedule")
	key := scheduleKey(in.GroupName, in.Name)
	current, ok := a.schedules[key]
	if !ok {
		return nil, &schedulertypes.ResourceNotFoundException{Message: aws.String(fmt.Sprintf("no schedule %s", key))}
	}
	current.input.ScheduleExpression, current.input.Target = in.ScheduleExpression, in.Target
	return &scheduler.UpdateScheduleOutput{}, nil
}

func (a schedulerAPI) DeleteSchedule(_ context.Context, in *scheduler.DeleteScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteSchedule")
	key := scheduleKey(in.GroupName, in.Name)
	if _, ok := a.schedules[key]; !ok {
		return nil, &schedulertypes.ResourceNotFoundException{Message: aws.String("no schedule")}
	}
	delete(a.schedules, key)
	return &scheduler.DeleteScheduleOutput{}, nil
}
