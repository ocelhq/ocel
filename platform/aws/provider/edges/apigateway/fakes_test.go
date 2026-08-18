package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

const (
	fakeStateTable  = "ocel-state"
	fakeAssetBucket = "ocel-assets"
	fakeRegion      = "eu-west-1"
	fakeAccount     = "123456789012"
)

type world struct {
	gateway *fakeGateway
	dynamo  *fakeDynamo
	iam     *fakeIAM
	cfn     *fakeCFN
}

func newWorld() *world {
	return &world{
		gateway: newFakeGateway(),
		dynamo:  newFakeDynamo(),
		iam:     newFakeIAM(),
		cfn:     &fakeCFN{},
	}
}

func (w *world) clients() Clients {
	return Clients{APIGateway: w.gateway, Dynamo: w.dynamo, IAM: w.iam, CFN: w.cfn, Region: fakeRegion}
}

func (w *world) edge() *provider {
	return &provider{open: func(context.Context) (Clients, error) { return w.clients(), nil }}
}

type fakeCFN struct {
	absent bool
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.absent {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName: in.StackName,
		Outputs: []cfntypes.Output{
			{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(fakeStateTable)},
			{OutputKey: aws.String("AssetBucketName"), OutputValue: aws.String(fakeAssetBucket)},
		},
	}}}, nil
}

type fakeIAM struct {
	mu       sync.Mutex
	roles    map[string]string
	policies map[string]string
	calls    []string
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{roles: map[string]string{}, policies: map[string]string{}}
}

func (f *fakeIAM) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.RoleName)
	f.record("GetRole " + name)
	arn, ok := f.roles[name]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role " + name)}
	}
	return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String(arn)}}, nil
}

func (f *fakeIAM) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.RoleName)
	f.record("CreateRole " + name)
	arn := "arn:aws:iam::" + fakeAccount + ":role/" + name
	f.roles[name] = arn
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{
		Arn:                      aws.String(arn),
		AssumeRolePolicyDocument: in.AssumeRolePolicyDocument,
	}}, nil
}

func (f *fakeIAM) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PutRolePolicy " + aws.ToString(in.RoleName))
	f.policies[aws.ToString(in.RoleName)] = aws.ToString(in.PolicyDocument)
	return &iam.PutRolePolicyOutput{}, nil
}

func (f *fakeIAM) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.RoleName)
	f.record("DeleteRolePolicy " + name)
	if _, ok := f.policies[name]; !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no policy on " + name)}
	}
	delete(f.policies, name)
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (f *fakeIAM) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.RoleName)
	f.record("DeleteRole " + name)
	if _, ok := f.roles[name]; !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no role " + name)}
	}
	delete(f.roles, name)
	return &iam.DeleteRoleOutput{}, nil
}

type fakeMethod struct {
	integration         agtypes.IntegrationType
	transfer            agtypes.ResponseTransferMode
	uri                 string
	credentials         string
	requestTemplates    map[string]string
	methodResponses     map[string]map[string]bool
	integrationResponse map[string]map[string]string
}

type fakeAPI struct {
	id        string
	name      string
	binary    []string
	resources map[string]string
	methods   map[string]*fakeMethod
	stage     string
	variables map[string]string
}

type fakeDomain struct {
	name        string
	certificate string
	mappings    map[string]agtypes.BasePathMapping
}

type fakeGateway struct {
	mu       sync.Mutex
	next     int
	apis     map[string]*fakeAPI
	domains  map[string]*fakeDomain
	calls    []string
	pageSize int

	createErr   error
	stageErr    error
	resourceErr error
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{apis: map[string]*fakeAPI{}, domains: map[string]*fakeDomain{}, pageSize: 1}
}

func page[T any](items []T, position *string, size int) ([]T, *string, error) {
	from := 0
	if at := aws.ToString(position); at != "" {
		parsed, err := strconv.Atoi(at)
		if err != nil {
			return nil, nil, fmt.Errorf("fake gateway got the position %q, which it never handed out", at)
		}
		from = parsed
	}
	if from > len(items) {
		return nil, nil, fmt.Errorf("fake gateway got the position %d, past the %d items it holds", from, len(items))
	}
	to := min(from+size, len(items))
	if to == len(items) {
		return items[from:to], nil, nil
	}
	return items[from:to], aws.String(strconv.Itoa(to)), nil
}

func (f *fakeGateway) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeGateway) mutations() []string {
	var out []string
	for _, call := range f.calls {
		verb, _, _ := strings.Cut(call, " ")
		if strings.HasPrefix(verb, "Get") {
			continue
		}
		out = append(out, call)
	}
	return out
}

func (f *fakeGateway) count(verb string) int {
	n := 0
	for _, call := range f.calls {
		if call == verb || strings.HasPrefix(call, verb+" ") {
			n++
		}
	}
	return n
}

func (f *fakeGateway) named(name string) *fakeAPI {
	for _, api := range f.apis {
		if api.name == name {
			return api
		}
	}
	return nil
}

func (f *fakeGateway) GetRestApis(_ context.Context, in *apigateway.GetRestApisInput, _ ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetRestApis")
	items := make([]agtypes.RestApi, 0, len(f.apis))
	for _, id := range slices.Sorted(maps.Keys(f.apis)) {
		api := f.apis[id]
		items = append(items, agtypes.RestApi{Id: aws.String(api.id), Name: aws.String(api.name)})
	}
	items, position, err := page(items, in.Position, f.pageSize)
	if err != nil {
		return nil, err
	}
	return &apigateway.GetRestApisOutput{Items: items, Position: position}, nil
}

func (f *fakeGateway) CreateRestApi(_ context.Context, in *apigateway.CreateRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("CreateRestApi " + name)
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.next++
	id := "api" + strconv.Itoa(f.next)
	f.apis[id] = &fakeAPI{
		id:        id,
		name:      name,
		binary:    in.BinaryMediaTypes,
		resources: map[string]string{id + "-root": "/"},
		methods:   map[string]*fakeMethod{},
		variables: map[string]string{},
	}
	return &apigateway.CreateRestApiOutput{Id: aws.String(id), Name: in.Name}, nil
}

func (f *fakeGateway) DeleteRestApi(_ context.Context, in *apigateway.DeleteRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.RestApiId)
	f.record("DeleteRestApi " + id)
	if _, ok := f.apis[id]; !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api " + id)}
	}
	delete(f.apis, id)
	return &apigateway.DeleteRestApiOutput{}, nil
}

func (f *fakeGateway) GetResources(_ context.Context, in *apigateway.GetResourcesInput, _ ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetResources")
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	items := make([]agtypes.Resource, 0, len(api.resources))
	for _, id := range slices.Sorted(maps.Keys(api.resources)) {
		items = append(items, agtypes.Resource{Id: aws.String(id), Path: aws.String(api.resources[id])})
	}
	items, position, err := page(items, in.Position, f.pageSize)
	if err != nil {
		return nil, err
	}
	return &apigateway.GetResourcesOutput{Items: items, Position: position}, nil
}

func (f *fakeGateway) CreateResource(_ context.Context, in *apigateway.CreateResourceInput, _ ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	parent := api.resources[aws.ToString(in.ParentId)]
	path := strings.TrimSuffix(parent, "/") + "/" + aws.ToString(in.PathPart)
	f.record("CreateResource " + path)
	if f.resourceErr != nil {
		return nil, f.resourceErr
	}
	id := api.id + path
	api.resources[id] = path
	return &apigateway.CreateResourceOutput{Id: aws.String(id), Path: aws.String(path)}, nil
}

func (f *fakeGateway) method(api *fakeAPI, resource, httpMethod string) *fakeMethod {
	key := resource + " " + httpMethod
	m, ok := api.methods[key]
	if !ok {
		m = &fakeMethod{
			methodResponses:     map[string]map[string]bool{},
			integrationResponse: map[string]map[string]string{},
		}
		api.methods[key] = m
	}
	return m
}

func (f *fakeGateway) PutMethod(_ context.Context, in *apigateway.PutMethodInput, _ ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	f.record("PutMethod " + api.resources[aws.ToString(in.ResourceId)] + " " + aws.ToString(in.HttpMethod))
	f.method(api, aws.ToString(in.ResourceId), aws.ToString(in.HttpMethod))
	return &apigateway.PutMethodOutput{}, nil
}

func (f *fakeGateway) PutIntegration(_ context.Context, in *apigateway.PutIntegrationInput, _ ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	f.record("PutIntegration " + api.resources[aws.ToString(in.ResourceId)] + " " + string(in.Type))
	m := f.method(api, aws.ToString(in.ResourceId), aws.ToString(in.HttpMethod))
	m.integration = in.Type
	m.transfer = in.ResponseTransferMode
	m.uri = aws.ToString(in.Uri)
	m.credentials = aws.ToString(in.Credentials)
	m.requestTemplates = in.RequestTemplates
	return &apigateway.PutIntegrationOutput{}, nil
}

func (f *fakeGateway) PutMethodResponse(_ context.Context, in *apigateway.PutMethodResponseInput, _ ...func(*apigateway.Options)) (*apigateway.PutMethodResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	f.record("PutMethodResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + aws.ToString(in.StatusCode))
	m := f.method(api, aws.ToString(in.ResourceId), aws.ToString(in.HttpMethod))
	m.methodResponses[aws.ToString(in.StatusCode)] = in.ResponseParameters
	return &apigateway.PutMethodResponseOutput{}, nil
}

func (f *fakeGateway) PutIntegrationResponse(_ context.Context, in *apigateway.PutIntegrationResponseInput, _ ...func(*apigateway.Options)) (*apigateway.PutIntegrationResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	f.record("PutIntegrationResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + aws.ToString(in.StatusCode))
	m := f.method(api, aws.ToString(in.ResourceId), aws.ToString(in.HttpMethod))
	m.integrationResponse[aws.ToString(in.StatusCode)] = in.ResponseParameters
	return &apigateway.PutIntegrationResponseOutput{}, nil
}

func (f *fakeGateway) CreateDeployment(_ context.Context, in *apigateway.CreateDeploymentInput, _ ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	f.record("CreateDeployment " + api.name)
	api.stage = aws.ToString(in.StageName)
	if in.Variables != nil {
		api.variables = map[string]string{}
		for name, value := range in.Variables {
			api.variables[name] = value
		}
	}
	return &apigateway.CreateDeploymentOutput{}, nil
}

func (f *fakeGateway) GetStage(_ context.Context, in *apigateway.GetStageInput, _ ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.StageName)
	f.record("GetStage " + name)
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok || api.stage != name {
		return nil, &agtypes.NotFoundException{Message: aws.String("no stage " + name)}
	}
	return &apigateway.GetStageOutput{StageName: in.StageName, Variables: api.variables}, nil
}

func (f *fakeGateway) UpdateStage(_ context.Context, in *apigateway.UpdateStageInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("UpdateStage " + aws.ToString(in.RestApiId))
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	for _, op := range in.PatchOperations {
		name := strings.TrimPrefix(aws.ToString(op.Path), "/variables/")
		api.variables[name] = aws.ToString(op.Value)
	}
	return &apigateway.UpdateStageOutput{}, nil
}

func (f *fakeGateway) GetDomainName(_ context.Context, in *apigateway.GetDomainNameInput, _ ...func(*apigateway.Options)) (*apigateway.GetDomainNameOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("GetDomainName " + name)
	domain, ok := f.domains[name]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	return &apigateway.GetDomainNameOutput{
		DomainName:             aws.String(domain.name),
		RegionalCertificateArn: aws.String(domain.certificate),
	}, nil
}

func (f *fakeGateway) CreateDomainName(_ context.Context, in *apigateway.CreateDomainNameInput, _ ...func(*apigateway.Options)) (*apigateway.CreateDomainNameOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("CreateDomainName " + name)
	f.domains[name] = &fakeDomain{
		name:        name,
		certificate: aws.ToString(in.RegionalCertificateArn),
		mappings:    map[string]agtypes.BasePathMapping{},
	}
	return &apigateway.CreateDomainNameOutput{DomainName: in.DomainName}, nil
}

func (f *fakeGateway) DeleteDomainName(_ context.Context, in *apigateway.DeleteDomainNameInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteDomainNameOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("DeleteDomainName " + name)
	if _, ok := f.domains[name]; !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	delete(f.domains, name)
	return &apigateway.DeleteDomainNameOutput{}, nil
}

func (f *fakeGateway) GetBasePathMappings(_ context.Context, in *apigateway.GetBasePathMappingsInput, _ ...func(*apigateway.Options)) (*apigateway.GetBasePathMappingsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("GetBasePathMappings " + name)
	domain, ok := f.domains[name]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	items := make([]agtypes.BasePathMapping, 0, len(domain.mappings))
	for _, base := range slices.Sorted(maps.Keys(domain.mappings)) {
		items = append(items, domain.mappings[base])
	}
	items, position, err := page(items, in.Position, f.pageSize)
	if err != nil {
		return nil, err
	}
	return &apigateway.GetBasePathMappingsOutput{Items: items, Position: position}, nil
}

func (f *fakeGateway) CreateBasePathMapping(_ context.Context, in *apigateway.CreateBasePathMappingInput, _ ...func(*apigateway.Options)) (*apigateway.CreateBasePathMappingOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("CreateBasePathMapping " + name)
	domain, ok := f.domains[name]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	base := aws.ToString(in.BasePath)
	if base == "" {
		base = "(none)"
	}
	domain.mappings[base] = agtypes.BasePathMapping{
		BasePath:  aws.String(base),
		RestApiId: in.RestApiId,
		Stage:     in.Stage,
	}
	return &apigateway.CreateBasePathMappingOutput{}, nil
}

func (f *fakeGateway) DeleteBasePathMapping(_ context.Context, in *apigateway.DeleteBasePathMappingInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteBasePathMappingOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("DeleteBasePathMapping " + name)
	domain, ok := f.domains[name]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	delete(domain.mappings, aws.ToString(in.BasePath))
	return &apigateway.DeleteBasePathMappingOutput{}, nil
}

type fakeDynamo struct {
	mu       sync.Mutex
	items    map[string]map[string]ddbtypes.AttributeValue
	calls    []string
	pageSize int

	putErr error
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}, pageSize: 1}
}

func dynamoKey(item map[string]ddbtypes.AttributeValue) string {
	pk, _ := item["pk"].(*ddbtypes.AttributeValueMemberS)
	sk, _ := item["sk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil || sk == nil {
		return ""
	}
	return pk.Value + "\x00" + sk.Value
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &dynamodb.GetItemOutput{Item: f.items[dynamoKey(in.Key)]}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return nil, f.putErr
	}
	key := dynamoKey(in.Item)
	held, err := conditionHolds(in, f.items[key])
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key)}
	}
	f.calls = append(f.calls, "PutItem "+key)
	f.items[key] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func conditionHolds(in *dynamodb.PutItemInput, present map[string]ddbtypes.AttributeValue) (bool, error) {
	condition := aws.ToString(in.ConditionExpression)
	switch condition {
	case "":
		return true, nil
	case "attribute_not_exists(#promotionId)":
		_, taken := present["promotionId"]
		return !taken, nil
	case "#promotionId = :promotionId", "#promotionId = :held":
		want, ok := in.ExpressionAttributeValues[":held"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("fake dynamo needs the value the condition %q compares against", condition)
		}
		got, ok := present["promotionId"].(*ddbtypes.AttributeValueMemberS)
		return ok && got.Value == want.Value, nil
	default:
		return false, fmt.Errorf("fake dynamo does not speak the condition %q", condition)
	}
}

func (f *fakeDynamo) BatchWriteItem(_ context.Context, in *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for table, requests := range in.RequestItems {
		if len(requests) > 25 {
			return nil, fmt.Errorf("fake dynamo got %d writes for %s, more than the 25 BatchWriteItem takes", len(requests), table)
		}
		for _, request := range requests {
			if request.DeleteRequest == nil {
				return nil, fmt.Errorf("fake dynamo batches only deletes, got %+v", request)
			}
			key := dynamoKey(request.DeleteRequest.Key)
			f.calls = append(f.calls, "DeleteItem "+key)
			delete(f.items, key)
		}
	}
	return &dynamodb.BatchWriteItemOutput{}, nil
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := dynamoKey(in.Key)
	f.calls = append(f.calls, "DeleteItem "+key)
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if aws.ToString(in.UpdateExpression) != "ADD #seq :one" {
		return nil, fmt.Errorf("fake dynamo understands only the sequence counter, got %q", aws.ToString(in.UpdateExpression))
	}
	key := dynamoKey(in.Key)
	item, ok := f.items[key]
	if !ok {
		item = map[string]ddbtypes.AttributeValue{}
		for name, value := range in.Key {
			item[name] = value
		}
	}
	current := int64(0)
	if seq, ok := item["seq"].(*ddbtypes.AttributeValueMemberN); ok {
		current, _ = strconv.ParseInt(seq.Value, 10, 64)
	}
	step := int64(1)
	if one, ok := in.ExpressionAttributeValues[":one"].(*ddbtypes.AttributeValueMemberN); ok {
		step, _ = strconv.ParseInt(one.Value, 10, 64)
	}
	next := &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(current+step, 10)}
	item["seq"] = next
	f.items[key] = item
	return &dynamodb.UpdateItemOutput{Attributes: map[string]ddbtypes.AttributeValue{"seq": next}}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	condition := aws.ToString(in.KeyConditionExpression)
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil {
		return nil, fmt.Errorf("fake dynamo needs a partition, got %q", condition)
	}
	prefix := ""
	switch condition {
	case "#pk = :pk":
	case "#pk = :pk AND begins_with(#sk, :prefix)":
		value, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
		if value == nil {
			return nil, fmt.Errorf("fake dynamo needs a prefix for %q", condition)
		}
		prefix = value.Value
	default:
		return nil, fmt.Errorf("fake dynamo does not speak the key condition %q", condition)
	}

	var items []map[string]ddbtypes.AttributeValue
	for _, key := range slices.Sorted(maps.Keys(f.items)) {
		partition, sort, _ := strings.Cut(key, "\x00")
		if partition != pk.Value || !strings.HasPrefix(sort, prefix) {
			continue
		}
		if after := dynamoKey(in.ExclusiveStartKey); after != "" && key <= after {
			continue
		}
		items = append(items, f.items[key])
		if len(items) == f.pageSize {
			return &dynamodb.QueryOutput{Items: items, LastEvaluatedKey: lastKey(f.items[key])}, nil
		}
	}
	return &dynamodb.QueryOutput{Items: items}, nil
}

func lastKey(item map[string]ddbtypes.AttributeValue) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{"pk": item["pk"], "sk": item["sk"]}
}
