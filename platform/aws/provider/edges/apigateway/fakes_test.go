package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	fakeStateTable  = "ocel-state"
	fakeAssetBucket = "ocel-assets"
	fakeRegion      = "eu-west-1"
	fakeAccount     = "123456789012"
	fakeInvokeRole  = "arn:aws:iam::" + fakeAccount + ":role/ocel-edge-invoke"
	fakeNotFoundAPI = "nf404"
)

type world struct {
	gateway *fakeGateway
	routing *fakeRouting
	dynamo  *fakeDynamo
	cfn     *fakeCFN
}

func newWorld() *world {
	gateway := newFakeGateway()
	return &world{
		gateway: gateway,
		routing: &fakeRouting{g: gateway},
		dynamo:  newFakeDynamo(),
		cfn:     &fakeCFN{},
	}
}

func (w *world) clients() Clients {
	return Clients{APIGateway: w.gateway, Routing: w.routing, Dynamo: w.dynamo, CFN: w.cfn, Region: fakeRegion}
}

func (w *world) edge() *provider {
	return &provider{
		open:   func(context.Context) (Clients, error) { return w.clients(), nil },
		delete: w.deleter(30),
	}
}

func (w *world) deleter(attempts int) *Deleter {
	return &Deleter{
		Wait: func(_ context.Context, held time.Duration) error {
			w.gateway.note("hold " + held.String())
			return nil
		},
		Attempts: attempts,
		Every:    30 * time.Second,
		Jitter:   func() float64 { return 0 },
	}
}

type fakeCFN struct {
	absent    bool
	otherEdge bool
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.absent {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	name, _ := strings.CutSuffix(aws.ToString(in.StackName), "-"+string(edge.ClassPreview))
	var outputs []cfntypes.Output
	switch name {
	case bootstrap.StackName:
		outputs = []cfntypes.Output{
			{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(fakeStateTable)},
			{OutputKey: aws.String("AssetBucketName"), OutputValue: aws.String(fakeAssetBucket)},
		}
	case bootstrap.StackName + "-" + bootstrap.FeatureAPIGatewayEdge:
		if f.otherEdge {
			return &cloudformation.DescribeStacksOutput{}, nil
		}
		outputs = []cfntypes.Output{
			{OutputKey: aws.String(bootstrap.OutputEdgeInvokeRoleARN), OutputValue: aws.String(fakeInvokeRole)},
			{OutputKey: aws.String(bootstrap.OutputEdgeNotFoundAPIID), OutputValue: aws.String(fakeNotFoundAPI)},
		}
	default:
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName: in.StackName,
		Outputs:   outputs,
	}}}, nil
}

type fakeMethod struct {
	open                bool
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
	routing     agtypes.RoutingMode
	mappings    map[string]agtypes.BasePathMapping
	rules       map[string]*fakeRule
}

type fakeRule struct {
	id       string
	priority int32
	header   string
	host     string
	api      string
	stage    string
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

	deleteErr     error
	deleteRefused int

	beforeRule func(*fakeGateway, int32)
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

func (f *fakeGateway) note(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(call)
}

func (f *fakeGateway) mutations() []string {
	var out []string
	for _, call := range f.calls {
		verb, _, _ := strings.Cut(call, " ")
		if strings.HasPrefix(verb, "Get") || strings.HasPrefix(verb, "List") {
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
	if f.deleteRefused > 0 {
		f.deleteRefused--
		return nil, f.deleteErr
	}
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
	m := f.method(api, aws.ToString(in.ResourceId), aws.ToString(in.HttpMethod))
	if m.open {
		return nil, &agtypes.ConflictException{Message: aws.String("Method already exists for this resource")}
	}
	m.open = true
	return &apigateway.PutMethodOutput{}, nil
}

func (f *fakeGateway) GetMethod(_ context.Context, in *apigateway.GetMethodInput, _ ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	httpMethod := aws.ToString(in.HttpMethod)
	f.record("GetMethod " + api.resources[aws.ToString(in.ResourceId)] + " " + httpMethod)
	m, ok := api.methods[aws.ToString(in.ResourceId)+" "+httpMethod]
	if !ok || !m.open {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method " + httpMethod)}
	}
	return &apigateway.GetMethodOutput{HttpMethod: in.HttpMethod}, nil
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
	if _, held := m.methodResponses[aws.ToString(in.StatusCode)]; held {
		return nil, &agtypes.ConflictException{Message: aws.String("Method response already exists for this method")}
	}
	m.methodResponses[aws.ToString(in.StatusCode)] = in.ResponseParameters
	return &apigateway.PutMethodResponseOutput{}, nil
}

func (f *fakeGateway) GetMethodResponse(_ context.Context, in *apigateway.GetMethodResponseInput, _ ...func(*apigateway.Options)) (*apigateway.GetMethodResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	status := aws.ToString(in.StatusCode)
	f.record("GetMethodResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + status)
	m, ok := api.methods[aws.ToString(in.ResourceId)+" "+aws.ToString(in.HttpMethod)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method")}
	}
	parameters, held := m.methodResponses[status]
	if !held {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method response " + status)}
	}
	return &apigateway.GetMethodResponseOutput{StatusCode: in.StatusCode, ResponseParameters: parameters}, nil
}

func (f *fakeGateway) DeleteMethodResponse(_ context.Context, in *apigateway.DeleteMethodResponseInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteMethodResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	status := aws.ToString(in.StatusCode)
	f.record("DeleteMethodResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + status)
	m, ok := api.methods[aws.ToString(in.ResourceId)+" "+aws.ToString(in.HttpMethod)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method")}
	}
	if _, held := m.methodResponses[status]; !held {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method response " + status)}
	}
	delete(m.methodResponses, status)
	return &apigateway.DeleteMethodResponseOutput{}, nil
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
	if _, held := m.integrationResponse[aws.ToString(in.StatusCode)]; held {
		return nil, &agtypes.ConflictException{Message: aws.String("Integration response already exists for this method")}
	}
	m.integrationResponse[aws.ToString(in.StatusCode)] = in.ResponseParameters
	return &apigateway.PutIntegrationResponseOutput{}, nil
}

func (f *fakeGateway) GetIntegrationResponse(_ context.Context, in *apigateway.GetIntegrationResponseInput, _ ...func(*apigateway.Options)) (*apigateway.GetIntegrationResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	status := aws.ToString(in.StatusCode)
	f.record("GetIntegrationResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + status)
	m, ok := api.methods[aws.ToString(in.ResourceId)+" "+aws.ToString(in.HttpMethod)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method")}
	}
	parameters, held := m.integrationResponse[status]
	if !held {
		return nil, &agtypes.NotFoundException{Message: aws.String("no integration response " + status)}
	}
	return &apigateway.GetIntegrationResponseOutput{StatusCode: in.StatusCode, ResponseParameters: parameters}, nil
}

func (f *fakeGateway) DeleteIntegrationResponse(_ context.Context, in *apigateway.DeleteIntegrationResponseInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteIntegrationResponseOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api, ok := f.apis[aws.ToString(in.RestApiId)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	status := aws.ToString(in.StatusCode)
	f.record("DeleteIntegrationResponse " + api.resources[aws.ToString(in.ResourceId)] + " " + status)
	m, ok := api.methods[aws.ToString(in.ResourceId)+" "+aws.ToString(in.HttpMethod)]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no method")}
	}
	if _, held := m.integrationResponse[status]; !held {
		return nil, &agtypes.NotFoundException{Message: aws.String("no integration response " + status)}
	}
	delete(m.integrationResponse, status)
	return &apigateway.DeleteIntegrationResponseOutput{}, nil
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
		RegionalDomainName:     aws.String(regionalFront(name)),
		RoutingMode:            domain.routing,
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
		routing:     in.RoutingMode,
		mappings:    map[string]agtypes.BasePathMapping{},
		rules:       map[string]*fakeRule{},
	}
	return &apigateway.CreateDomainNameOutput{
		DomainName:         in.DomainName,
		RegionalDomainName: aws.String(regionalFront(name)),
	}, nil
}

func (f *fakeGateway) UpdateDomainName(_ context.Context, in *apigateway.UpdateDomainNameInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateDomainNameOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.record("UpdateDomainName " + name)
	domain, ok := f.domains[name]
	if !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no domain " + name)}
	}
	for _, op := range in.PatchOperations {
		path, value := aws.ToString(op.Path), aws.ToString(op.Value)
		if op.Op != agtypes.OpReplace {
			return nil, fmt.Errorf("fake gateway got op %q on %s; API Gateway supports only replace on a domain name's certificate and routing mode", op.Op, path)
		}
		switch path {
		case "/regionalCertificateArn":
			domain.certificate = value
		case "/routingMode":
			domain.routing = agtypes.RoutingMode(value)
		default:
			return nil, fmt.Errorf("fake gateway got the patch path %q, which UpdateDomainName does not support", path)
		}
	}
	return &apigateway.UpdateDomainNameOutput{
		DomainName:             aws.String(domain.name),
		RegionalCertificateArn: aws.String(domain.certificate),
		RegionalDomainName:     aws.String(regionalFront(name)),
	}, nil
}

func regionalFront(name string) string {
	return "d-" + strings.NewReplacer("*", "wild", ".", "-").Replace(name) + ".execute-api." + fakeRegion + ".amazonaws.com"
}

type fakeRouting struct {
	g *fakeGateway
}

func (f *fakeRouting) ListRoutingRules(_ context.Context, in *apigatewayv2.ListRoutingRulesInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.ListRoutingRulesOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.g.record("ListRoutingRules " + name)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	items := make([]agv2types.RoutingRule, 0, len(domain.rules))
	for _, id := range slices.Sorted(maps.Keys(domain.rules)) {
		rule := domain.rules[id]
		items = append(items, agv2types.RoutingRule{
			RoutingRuleId: aws.String(rule.id),
			Priority:      aws.Int32(rule.priority),
			Conditions: []agv2types.RoutingRuleCondition{{
				MatchHeaders: &agv2types.RoutingRuleMatchHeaders{
					AnyOf: []agv2types.RoutingRuleMatchHeaderValue{{
						Header:    aws.String(rule.header),
						ValueGlob: aws.String(rule.host),
					}},
				},
			}},
			Actions: []agv2types.RoutingRuleAction{{
				InvokeApi: &agv2types.RoutingRuleActionInvokeApi{
					ApiId: aws.String(rule.api),
					Stage: aws.String(rule.stage),
				},
			}},
		})
	}
	items, token, err := page(items, in.NextToken, f.g.pageSize)
	if err != nil {
		return nil, err
	}
	return &apigatewayv2.ListRoutingRulesOutput{RoutingRules: items, NextToken: token}, nil
}

func hostConditionOf(conditions []agv2types.RoutingRuleCondition) (header, host string) {
	for _, condition := range conditions {
		if condition.MatchHeaders == nil {
			continue
		}
		for _, held := range condition.MatchHeaders.AnyOf {
			if strings.EqualFold(aws.ToString(held.Header), "host") {
				header, host = aws.ToString(held.Header), aws.ToString(held.ValueGlob)
			}
		}
	}
	return header, host
}

func checkRuleInput(domain *fakeDomain, host string, priority int32, conditions []agv2types.RoutingRuleCondition, actions []agv2types.RoutingRuleAction, self string) error {
	if host == "" {
		return fmt.Errorf("fake routing needs a host condition, got %+v", conditions)
	}
	if len(actions) != 1 || actions[0].InvokeApi == nil {
		return fmt.Errorf("fake routing serves only the invoke-api action, got %+v", actions)
	}
	if priority < 1 || priority > 1_000_000 {
		return fmt.Errorf("fake routing got priority %d, outside the 1-1,000,000 API Gateway allows", priority)
	}
	for _, held := range domain.rules {
		if held.priority == priority && held.id != self {
			return &agv2types.ConflictException{Message: aws.String(fmt.Sprintf("priority %d is already held by %s", priority, held.id))}
		}
	}
	return nil
}

func (f *fakeRouting) CreateRoutingRule(_ context.Context, in *apigatewayv2.CreateRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.CreateRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name := aws.ToString(in.DomainName)
	header, host := hostConditionOf(in.Conditions)
	f.g.record("CreateRoutingRule " + name + " " + host)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	priority := aws.ToInt32(in.Priority)
	if f.g.beforeRule != nil {
		f.g.beforeRule(f.g, priority)
	}
	if err := checkRuleInput(domain, host, priority, in.Conditions, in.Actions, ""); err != nil {
		return nil, err
	}
	f.g.next++
	id := "rule" + strconv.Itoa(f.g.next)
	domain.rules[id] = &fakeRule{
		id:       id,
		priority: priority,
		header:   header,
		host:     host,
		api:      aws.ToString(in.Actions[0].InvokeApi.ApiId),
		stage:    aws.ToString(in.Actions[0].InvokeApi.Stage),
	}
	return &apigatewayv2.CreateRoutingRuleOutput{RoutingRuleId: aws.String(id)}, nil
}

func (f *fakeRouting) PutRoutingRule(_ context.Context, in *apigatewayv2.PutRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.PutRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name, id := aws.ToString(in.DomainName), aws.ToString(in.RoutingRuleId)
	header, host := hostConditionOf(in.Conditions)
	f.g.record("PutRoutingRule " + name + " " + host)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	rule, ok := domain.rules[id]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no rule " + id)}
	}
	priority := aws.ToInt32(in.Priority)
	if err := checkRuleInput(domain, host, priority, in.Conditions, in.Actions, id); err != nil {
		return nil, err
	}
	rule.priority, rule.header, rule.host = priority, header, host
	rule.api, rule.stage = aws.ToString(in.Actions[0].InvokeApi.ApiId), aws.ToString(in.Actions[0].InvokeApi.Stage)
	return &apigatewayv2.PutRoutingRuleOutput{RoutingRuleId: aws.String(id)}, nil
}

func (f *fakeRouting) DeleteRoutingRule(_ context.Context, in *apigatewayv2.DeleteRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.DeleteRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name, id := aws.ToString(in.DomainName), aws.ToString(in.RoutingRuleId)
	domain, ok := f.g.domains[name]
	if !ok {
		f.g.record("DeleteRoutingRule " + name + " " + id)
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	rule, ok := domain.rules[id]
	if !ok {
		f.g.record("DeleteRoutingRule " + name + " " + id)
		return nil, &agv2types.NotFoundException{Message: aws.String("no rule " + id)}
	}
	f.g.record("DeleteRoutingRule " + name + " " + rule.host)
	delete(domain.rules, id)
	return &apigatewayv2.DeleteRoutingRuleOutput{}, nil
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
	case "attribute_not_exists(#pk)":
		return len(present) == 0, nil
	case "#rev = :rev":
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", condition)
		}
		got, ok := present["rev"].(*ddbtypes.AttributeValueMemberS)
		return ok && got.Value == want.Value, nil
	default:
		return false, fmt.Errorf("fake dynamo does not speak the condition %q", condition)
	}
}

func (f *fakeDynamo) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return nil, fmt.Errorf("fake dynamo writes no transaction: nothing this suite drives writes a pair")
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := dynamoKey(in.Key)
	held, present := f.items[key]
	if aws.ToString(in.ConditionExpression) != "" {
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", aws.ToString(in.ConditionExpression))
		}
		got, matched := held["rev"].(*ddbtypes.AttributeValueMemberS)
		if !present {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("no " + key)}
		}
		if !matched || got.Value != want.Value {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key), Item: held}
		}
	}
	f.calls = append(f.calls, "DeleteItem "+key)
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
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

func ownState(t *testing.T, stack edge.EdgeStack) private {
	t.Helper()

	var own private
	if err := stack.State().Adapter.Into(&own); err != nil {
		t.Fatalf("read the state the edge keeps to itself: %v", err)
	}
	return own
}
