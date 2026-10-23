package functiontest

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

	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart"
)

const (
	AccountID = "123456789012"
	Region    = "us-east-1"
)

type Role struct {
	ID        int
	Path      string
	Boundary  string
	MadeUnder string
	Trust     string
	Tags      map[string]string
	Policies  map[string]string
}

type Function struct {
	Input  lambda.CreateFunctionInput
	RoleID int
	Code   []byte
	Tags   map[string]string
	URL    *lambda.CreateFunctionUrlConfigInput
}

type Mapping struct {
	UUID  string
	Input lambda.CreateEventSourceMappingInput
}

type Account struct {
	mu sync.Mutex

	Roles     map[string]*Role
	Functions map[string]*Function
	LogGroups map[string]map[string]string
	Retention map[string]int32
	Mappings  map[string]*Mapping

	Unassumable int
	Unpermitted int
	Updating    int
	RoleIDs     int
	Calls       []string
}

func New() *Account {
	return &Account{
		Roles:     map[string]*Role{},
		Functions: map[string]*Function{},
		LogGroups: map[string]map[string]string{},
		Retention: map[string]int32{},
		Mappings:  map[string]*Mapping{},
	}
}

func (a *Account) Clients() lambdapart.Clients {
	return lambdapart.Clients{IAM: IAM{a}, Lambda: Lambda{a}, Logs: Logs{a}}
}

func (a *Account) Lambda() Lambda { return Lambda{a} }

func (a *Account) Called(call string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, made := range a.Calls {
		if made == call {
			n++
		}
	}
	return n
}

func (a *Account) record(call string) { a.Calls = append(a.Calls, call) }

func (a *Account) idOfRole(arn string) int {
	if r, ok := a.Roles[arn[strings.LastIndex(arn, "/")+1:]]; ok {
		return r.ID
	}
	return 0
}

func (a *Account) RunsAsItsRole(function string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.Functions[function]
	return ok && f.RoleID != 0 && f.RoleID == a.idOfRole(aws.ToString(f.Input.Role))
}

func FunctionARN(name string) string {
	return "arn:aws:lambda:" + Region + ":" + AccountID + ":function:" + name
}

func RoleARN(path, name string) string {
	return "arn:aws:iam::" + AccountID + ":role" + path + name
}

type IAM struct{ *Account }

func (a IAM) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateRole")
	name := aws.ToString(in.RoleName)
	if _, ok := a.Roles[name]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}
	}
	tags := map[string]string{}
	for _, tag := range in.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	a.RoleIDs++
	a.Roles[name] = &Role{ID: a.RoleIDs, Path: aws.ToString(in.Path), Boundary: aws.ToString(in.PermissionsBoundary), MadeUnder: aws.ToString(in.PermissionsBoundary), Trust: aws.ToString(in.AssumeRolePolicyDocument), Tags: tags, Policies: map[string]string{}}
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{RoleName: in.RoleName, Arn: aws.String(RoleARN(aws.ToString(in.Path), name))}}, nil
}

func (a IAM) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRolePolicy")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	r.Policies[aws.ToString(in.PolicyName)] = aws.ToString(in.PolicyDocument)
	return &iam.PutRolePolicyOutput{}, nil
}

func (a IAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("GetRole")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	role := &iamtypes.Role{RoleName: in.RoleName, AssumeRolePolicyDocument: aws.String(url.QueryEscape(r.Trust))}
	if r.Boundary != "" {
		role.PermissionsBoundary = &iamtypes.AttachedPermissionsBoundary{PermissionsBoundaryArn: aws.String(r.Boundary)}
	}
	return &iam.GetRoleOutput{Role: role}, nil
}

func (a IAM) UpdateAssumeRolePolicy(_ context.Context, in *iam.UpdateAssumeRolePolicyInput, _ ...func(*iam.Options)) (*iam.UpdateAssumeRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateAssumeRolePolicy")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	if r.Boundary != r.MadeUnder {
		return nil, fmt.Errorf("AccessDenied: role %s is not under %s, the boundary iam:UpdateAssumeRolePolicy is granted under", aws.ToString(in.RoleName), r.MadeUnder)
	}
	r.Trust = aws.ToString(in.PolicyDocument)
	return &iam.UpdateAssumeRolePolicyOutput{}, nil
}

func (a IAM) PutRolePermissionsBoundary(_ context.Context, in *iam.PutRolePermissionsBoundaryInput, _ ...func(*iam.Options)) (*iam.PutRolePermissionsBoundaryOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRolePermissionsBoundary")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	r.Boundary = aws.ToString(in.PermissionsBoundary)
	return &iam.PutRolePermissionsBoundaryOutput{}, nil
}

func (a IAM) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("ListRolePolicies")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	var names []string
	for name := range r.Policies {
		names = append(names, name)
	}
	return &iam.ListRolePoliciesOutput{PolicyNames: names}, nil
}

func (a IAM) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteRolePolicy")
	if r, ok := a.Roles[aws.ToString(in.RoleName)]; ok {
		delete(r.Policies, aws.ToString(in.PolicyName))
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (a IAM) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteRole")
	r, ok := a.Roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role")}
	}
	if len(r.Policies) > 0 {
		return nil, &iamtypes.DeleteConflictException{Message: aws.String("role has policies")}
	}
	delete(a.Roles, aws.ToString(in.RoleName))
	return &iam.DeleteRoleOutput{}, nil
}

type Lambda struct{ *Account }

func (a Lambda) CreateFunction(_ context.Context, in *lambda.CreateFunctionInput, _ ...func(*lambda.Options)) (*lambda.CreateFunctionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateFunction")
	if a.Unassumable > 0 {
		a.Unassumable--
		return nil, &lambdatypes.InvalidParameterValueException{Message: aws.String("The role defined for the function cannot be assumed by Lambda.")}
	}
	name := aws.ToString(in.FunctionName)
	if _, ok := a.Functions[name]; ok {
		return nil, &lambdatypes.ResourceConflictException{Message: aws.String("exists")}
	}
	a.Functions[name] = &Function{Input: *in, RoleID: a.idOfRole(aws.ToString(in.Role)), Code: in.Code.ZipFile, Tags: in.Tags}
	return &lambda.CreateFunctionOutput{FunctionArn: aws.String(FunctionARN(name))}, nil
}

func (a Lambda) updating() error {
	if a.Updating > 0 {
		a.Updating--
		return &lambdatypes.ResourceConflictException{Message: aws.String("An update is in progress for resource")}
	}
	return nil
}

func (a Lambda) UpdateFunctionCode(_ context.Context, in *lambda.UpdateFunctionCodeInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateFunctionCode")
	if err := a.updating(); err != nil {
		return nil, err
	}
	f, ok := a.Functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	f.Code = in.ZipFile
	return &lambda.UpdateFunctionCodeOutput{}, nil
}

func (a Lambda) UpdateFunctionConfiguration(_ context.Context, in *lambda.UpdateFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateFunctionConfiguration")
	if err := a.updating(); err != nil {
		return nil, err
	}
	f, ok := a.Functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	if in.Role != nil && aws.ToString(in.Role) != aws.ToString(f.Input.Role) {
		f.RoleID = a.idOfRole(aws.ToString(in.Role))
	}
	f.Input.Environment, f.Input.MemorySize, f.Input.Timeout, f.Input.Role = in.Environment, in.MemorySize, in.Timeout, in.Role
	f.Input.Description, f.Input.Handler, f.Input.Runtime, f.Input.LoggingConfig = in.Description, in.Handler, in.Runtime, in.LoggingConfig
	return &lambda.UpdateFunctionConfigurationOutput{}, nil
}

func (a Lambda) DeleteFunction(_ context.Context, in *lambda.DeleteFunctionInput, _ ...func(*lambda.Options)) (*lambda.DeleteFunctionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteFunction")
	name := aws.ToString(in.FunctionName)
	if _, ok := a.Functions[name]; !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	delete(a.Functions, name)
	for id, mapping := range a.Mappings {
		if aws.ToString(mapping.Input.FunctionName) == name {
			delete(a.Mappings, id)
		}
	}
	return &lambda.DeleteFunctionOutput{}, nil
}

func urlOf(name string) string { return "https://" + name + ".lambda-url." + Region + ".on.aws/" }

func (a Lambda) CreateFunctionUrlConfig(_ context.Context, in *lambda.CreateFunctionUrlConfigInput, _ ...func(*lambda.Options)) (*lambda.CreateFunctionUrlConfigOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateFunctionUrlConfig")
	f, ok := a.Functions[aws.ToString(in.FunctionName)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no function")}
	}
	if f.URL != nil {
		return nil, &lambdatypes.ResourceConflictException{Message: aws.String("url exists")}
	}
	f.URL = in
	return &lambda.CreateFunctionUrlConfigOutput{FunctionUrl: aws.String(urlOf(aws.ToString(in.FunctionName)))}, nil
}

func (a Lambda) UpdateFunctionUrlConfig(_ context.Context, in *lambda.UpdateFunctionUrlConfigInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionUrlConfigOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateFunctionUrlConfig")
	f, ok := a.Functions[aws.ToString(in.FunctionName)]
	if !ok || f.URL == nil {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no url")}
	}
	f.URL.AuthType, f.URL.InvokeMode = in.AuthType, in.InvokeMode
	return &lambda.UpdateFunctionUrlConfigOutput{FunctionUrl: aws.String(urlOf(aws.ToString(in.FunctionName)))}, nil
}

func (a Lambda) CreateEventSourceMapping(_ context.Context, in *lambda.CreateEventSourceMappingInput, _ ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateEventSourceMapping")
	if a.Unpermitted > 0 {
		a.Unpermitted--
		return nil, &lambdatypes.InvalidParameterValueException{Message: aws.String("Cannot access stream " + aws.ToString(in.EventSourceArn) + ". Please ensure the role can perform the GetRecords, GetShardIterator, DescribeStream, and ListStreams Actions on your stream in IAM.")}
	}
	for _, mapping := range a.Mappings {
		if aws.ToString(mapping.Input.FunctionName) == aws.ToString(in.FunctionName) && aws.ToString(mapping.Input.EventSourceArn) == aws.ToString(in.EventSourceArn) {
			return nil, &lambdatypes.ResourceConflictException{Message: aws.String("mapping exists")}
		}
	}
	id := "mapping-" + aws.ToString(in.FunctionName)
	a.Mappings[id] = &Mapping{UUID: id, Input: *in}
	return &lambda.CreateEventSourceMappingOutput{UUID: aws.String(id)}, nil
}

func (a Lambda) ListEventSourceMappings(_ context.Context, in *lambda.ListEventSourceMappingsInput, _ ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("ListEventSourceMappings")
	var out []lambdatypes.EventSourceMappingConfiguration
	for _, mapping := range a.Mappings {
		if in.FunctionName != nil && aws.ToString(mapping.Input.FunctionName) != aws.ToString(in.FunctionName) {
			continue
		}
		if in.EventSourceArn != nil && aws.ToString(mapping.Input.EventSourceArn) != aws.ToString(in.EventSourceArn) {
			continue
		}
		out = append(out, lambdatypes.EventSourceMappingConfiguration{UUID: aws.String(mapping.UUID), EventSourceArn: mapping.Input.EventSourceArn})
	}
	return &lambda.ListEventSourceMappingsOutput{EventSourceMappings: out}, nil
}

func (a Lambda) UpdateEventSourceMapping(_ context.Context, in *lambda.UpdateEventSourceMappingInput, _ ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("UpdateEventSourceMapping")
	mapping, ok := a.Mappings[aws.ToString(in.UUID)]
	if !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no mapping")}
	}
	mapping.Input.BatchSize, mapping.Input.FilterCriteria, mapping.Input.DestinationConfig = in.BatchSize, in.FilterCriteria, in.DestinationConfig
	mapping.Input.MaximumRetryAttempts, mapping.Input.FunctionResponseTypes = in.MaximumRetryAttempts, in.FunctionResponseTypes
	return &lambda.UpdateEventSourceMappingOutput{UUID: in.UUID}, nil
}

func (a Lambda) DeleteEventSourceMapping(_ context.Context, in *lambda.DeleteEventSourceMappingInput, _ ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteEventSourceMapping")
	if _, ok := a.Mappings[aws.ToString(in.UUID)]; !ok {
		return nil, &lambdatypes.ResourceNotFoundException{Message: aws.String("no mapping")}
	}
	delete(a.Mappings, aws.ToString(in.UUID))
	return &lambda.DeleteEventSourceMappingOutput{}, nil
}

type Logs struct{ *Account }

func (a Logs) CreateLogGroup(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("CreateLogGroup")
	name := aws.ToString(in.LogGroupName)
	if _, ok := a.LogGroups[name]; ok {
		return nil, &logstypes.ResourceAlreadyExistsException{Message: aws.String("exists")}
	}
	a.LogGroups[name] = in.Tags
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}

func (a Logs) PutRetentionPolicy(_ context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("PutRetentionPolicy")
	a.Retention[aws.ToString(in.LogGroupName)] = aws.ToInt32(in.RetentionInDays)
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}

func (a Logs) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteLogGroup")
	if _, ok := a.LogGroups[aws.ToString(in.LogGroupName)]; !ok {
		return nil, &logstypes.ResourceNotFoundException{Message: aws.String("no group")}
	}
	delete(a.LogGroups, aws.ToString(in.LogGroupName))
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}
