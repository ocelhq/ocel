package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Kind edge.Kind = "api-gateway"

const propagationBound = 5 * time.Second

const (
	EdgeHeader = "x-ocel-edge"

	edgeHeaderValue = string(Kind)

	stageName = bootstrap.EdgeStageName

	entryVariable  = "entry"
	assetsVariable = "assets"

	unsetVariable = "unset"
)

type APIGatewayAPI interface {
	GetRestApis(context.Context, *apigateway.GetRestApisInput, ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	CreateRestApi(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	DeleteRestApi(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	GetResources(context.Context, *apigateway.GetResourcesInput, ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	CreateResource(context.Context, *apigateway.CreateResourceInput, ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	GetMethod(context.Context, *apigateway.GetMethodInput, ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error)
	PutMethod(context.Context, *apigateway.PutMethodInput, ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	PutIntegration(context.Context, *apigateway.PutIntegrationInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
	GetMethodResponse(context.Context, *apigateway.GetMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.GetMethodResponseOutput, error)
	PutMethodResponse(context.Context, *apigateway.PutMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.PutMethodResponseOutput, error)
	DeleteMethodResponse(context.Context, *apigateway.DeleteMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.DeleteMethodResponseOutput, error)
	GetIntegrationResponse(context.Context, *apigateway.GetIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.GetIntegrationResponseOutput, error)
	PutIntegrationResponse(context.Context, *apigateway.PutIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationResponseOutput, error)
	DeleteIntegrationResponse(context.Context, *apigateway.DeleteIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.DeleteIntegrationResponseOutput, error)
	GetStage(context.Context, *apigateway.GetStageInput, ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error)
	CreateDeployment(context.Context, *apigateway.CreateDeploymentInput, ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	UpdateStage(context.Context, *apigateway.UpdateStageInput, ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error)
	GetDomainName(context.Context, *apigateway.GetDomainNameInput, ...func(*apigateway.Options)) (*apigateway.GetDomainNameOutput, error)
	CreateDomainName(context.Context, *apigateway.CreateDomainNameInput, ...func(*apigateway.Options)) (*apigateway.CreateDomainNameOutput, error)
	UpdateDomainName(context.Context, *apigateway.UpdateDomainNameInput, ...func(*apigateway.Options)) (*apigateway.UpdateDomainNameOutput, error)
	DeleteDomainName(context.Context, *apigateway.DeleteDomainNameInput, ...func(*apigateway.Options)) (*apigateway.DeleteDomainNameOutput, error)
	GetBasePathMappings(context.Context, *apigateway.GetBasePathMappingsInput, ...func(*apigateway.Options)) (*apigateway.GetBasePathMappingsOutput, error)
	CreateBasePathMapping(context.Context, *apigateway.CreateBasePathMappingInput, ...func(*apigateway.Options)) (*apigateway.CreateBasePathMappingOutput, error)
	DeleteBasePathMapping(context.Context, *apigateway.DeleteBasePathMappingInput, ...func(*apigateway.Options)) (*apigateway.DeleteBasePathMappingOutput, error)
}

type RoutingAPI interface {
	ListRoutingRules(context.Context, *apigatewayv2.ListRoutingRulesInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.ListRoutingRulesOutput, error)
	CreateRoutingRule(context.Context, *apigatewayv2.CreateRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.CreateRoutingRuleOutput, error)
	PutRoutingRule(context.Context, *apigatewayv2.PutRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.PutRoutingRuleOutput, error)
	DeleteRoutingRule(context.Context, *apigatewayv2.DeleteRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.DeleteRoutingRuleOutput, error)
}

type Clients struct {
	APIGateway APIGatewayAPI
	Routing    RoutingAPI
	Dynamo     awsports.DynamoAPI
	CFN        cfn.StacksAPI
	Region     string
}

type apiGateway struct {
	ns   bootstrap.Namespace
	open func(context.Context) (Clients, error)

	mu      sync.Mutex
	delete  *Deletion
	clients *Clients
}

func New(ns bootstrap.Namespace, open func(context.Context) (Clients, error)) edge.Edge {
	return &apiGateway{ns: ns, open: open, delete: NewDeletion()}
}

func FromConfig(load func(context.Context) (aws.Config, error)) func(context.Context) (Clients, error) {
	return func(ctx context.Context) (Clients, error) {
		if load == nil {
			return Clients{}, fmt.Errorf("the %q edge was built without a way to load AWS configuration", Kind)
		}
		awscfg, err := load(ctx)
		if err != nil {
			return Clients{}, err
		}
		return Clients{
			APIGateway: apigateway.NewFromConfig(awscfg),
			Routing:    apigatewayv2.NewFromConfig(awscfg),
			Dynamo:     dynamodb.NewFromConfig(awscfg),
			CFN:        cloudformation.NewFromConfig(awscfg),
			Region:     awscfg.Region,
		}, nil
	}
}

func (p *apiGateway) deletion() *Deletion {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.delete == nil {
		p.delete = NewDeletion()
	}
	return p.delete
}

func (p *apiGateway) Kind() edge.Kind { return Kind }

func (p *apiGateway) Facts() edge.Facts {
	return edge.Facts{
		Supported:           []edge.Need{edge.NeedStreaming},
		FlipBound:           edge.FlipBound{Typical: propagationBound},
		SignsOriginForwards: true,
	}
}

func (p *apiGateway) Hooks() edge.Hooks { return edge.Hooks{} }

func CertificateRegion(apiRegion string) string { return apiRegion }

const (
	typeRestAPI    = "AWS::ApiGateway::RestApi"
	typeDomainName = "AWS::ApiGateway::DomainName"
)

func (p *apiGateway) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	changes := []edge.PlanChange{{
		Kind:   typeRestAPI,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
		Reason: restAPIsReason(scope.Class),
		Slow:   true,
	}}
	for _, hostname := range scope.Hostnames {
		changes = append(changes, edge.PlanChange{
			Kind:   typeDomainName,
			Name:   hostname,
			Action: edge.PlanDelete,
		})
	}
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(Kind),
		Action:  edge.PlanDelete,
		Changes: changes,
	}}
}

func restAPIsReason(class edge.Class) string {
	const paced = "; API Gateway deletes at most one REST API every 30 seconds per account, so a project with many previews takes a while"
	if class == edge.ClassPreview {
		return "every preview API this project is served through, and the host rules routing to them" + paced
	}
	return "the production API and every preview API this project is served through, and the host rules routing to them" + paced
}

func (p *apiGateway) PreviewWildcardRemovals(wildcard string) (edge.PlanGroup, edge.PlanGroup) {
	removed := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind:   typeDomainName,
			Name:   wildcard,
			Action: edge.PlanDelete,
			Reason: "every project's previews are routed through it, and the rules under it go with it",
		}},
	}
	return removed, p.SharedPreviewRemoval()
}

func (p *apiGateway) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: the preview fallback API answers every preview hostname no project claims",
	}
}

func (p *apiGateway) clientsFor(ctx context.Context) (Clients, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients != nil {
		return *p.clients, nil
	}
	if p.open == nil {
		return Clients{}, fmt.Errorf("the %q edge has no AWS clients; it must be built with the provider's AWS configuration", Kind)
	}
	c, err := p.open(ctx)
	if err != nil {
		return Clients{}, fmt.Errorf("open the AWS clients the %q edge fronts deployments with: %w", Kind, err)
	}
	p.clients = &c
	return c, nil
}

func knownClass(class edge.Class) error {
	if class != edge.ClassProduction && class != edge.ClassPreview {
		return fmt.Errorf("the %q edge does not know the class %q", Kind, class)
	}
	return nil
}

func (p *apiGateway) bootstrap(ctx context.Context, c Clients, class edge.Class) (bootstrap.Deployed, error) {
	if err := knownClass(class); err != nil {
		return bootstrap.Deployed{}, err
	}
	if class == edge.ClassPreview {
		return bootstrap.CheckDeployedPreview(ctx, c.CFN, p.ns)
	}
	return bootstrap.CheckDeployed(ctx, c.CFN, p.ns)
}

func (p *apiGateway) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	if err := knownClass(class); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (p *apiGateway) Teardown(ctx context.Context, class edge.Class) error {
	if err := knownClass(class); err != nil {
		return err
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	names, err := restAPIs(ctx, c)
	if err != nil {
		return err
	}
	projects := surface.ProjectsNamed(p.ns, slices.Sorted(maps.Values(names)), class)
	if len(projects) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %q edge still fronts %d project(s) of class %s with a REST API of their own: %s. Run `%s` in each of them first, then take this bootstrap down",
		Kind, len(projects), class, strings.Join(projects, ", "), "ocel destroy "+string(class))
}

func (p *apiGateway) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	if spec.Slug == "" {
		return nil, fmt.Errorf("the %q edge fronts a project by slug; this stack names none", Kind)
	}
	deployed, err := p.bootstrap(ctx, c, spec.Class)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, fmt.Errorf("the %s bootstrap is not installed, so the %q edge has no state table to keep %s's deployments in", spec.Class, Kind, spec.Slug)
	}
	role, err := requireInvokeRole(p.ns, deployed, spec.Class)
	if err != nil {
		return nil, err
	}

	var own private
	if err := prior.Private.Into(&own); err != nil {
		return nil, err
	}
	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	own.StateTable = deployed.StateTable
	own.AssetBucket = deployed.AssetBucket
	own.Role = role
	own.Region = c.Region

	s := &stack{p: p, state: next, own: own}
	if err := s.ledger(c).EnsureSchema(ctx); err != nil {
		return nil, err
	}
	if spec.PruneOnly {
		return s, nil
	}
	id, err := s.reconcileAPI(ctx, c, "")
	if err != nil {
		return nil, err
	}
	s.own.API = id
	if err := s.publishDomainFronts(ctx, c, spec.Warn); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *apiGateway) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{p: p, state: state}
	if err := state.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *apiGateway) ProjectOwner(slug string, class edge.Class) string {
	return apiName(p.ns, slug, class, "")
}

func (p *apiGateway) DomainOwner(ctx context.Context, hostname string) (string, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return "", err
	}
	domain, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{DomainName: aws.String(hostname)})
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the API Gateway domain name for %s: %w", hostname, err)
	}
	if domain.RoutingMode == agtypes.RoutingModeRoutingRuleOnly {
		return catchAllOwner(ctx, c, hostname)
	}
	mappings, err := basePathMappings(ctx, c, hostname)
	if err != nil {
		return "", err
	}
	if len(mappings) == 0 {
		return "", nil
	}
	names, err := restAPIs(ctx, c)
	if err != nil {
		return "", err
	}
	for _, mapping := range mappings {
		id := aws.ToString(mapping.RestApiId)
		if id == "" {
			continue
		}
		if name := names[id]; name != "" {
			return name, nil
		}
		return id, nil
	}
	return "", nil
}

func apiName(ns bootstrap.Namespace, slug string, class edge.Class, pointer string) string {
	if p := pointerOr(pointer); p != edge.DefaultPointer {
		return surface.Name(ns, slug, class, p)
	}
	return surface.Name(ns, slug, class)
}

func pointerOr(pointer string) string {
	if pointer == "" {
		return edge.DefaultPointer
	}
	return pointer
}

func accountOf(roleARN string) string {
	parts := strings.Split(roleARN, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}
