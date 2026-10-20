package apigateway

import (
	"context"
	"fmt"
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
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
)

const Kind edge.Kind = "api-gateway"

const propagationBound = 5 * time.Second

const (
	routerHeaderValue = string(Kind)

	stageName = notfoundapi.StageName

	entryVariable  = "entry"
	assetsVariable = "assets"
	routesVariable = "routes"

	unsetVariable = "unset"
)

type APIGatewayAPI interface {
	GetRestApi(context.Context, *apigateway.GetRestApiInput, ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error)
	CreateRestApi(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	TagResource(context.Context, *apigateway.TagResourceInput, ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error)
	DeleteRestApi(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	GetResources(context.Context, *apigateway.GetResourcesInput, ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	CreateResource(context.Context, *apigateway.CreateResourceInput, ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	DeleteResource(context.Context, *apigateway.DeleteResourceInput, ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error)
	GetMethod(context.Context, *apigateway.GetMethodInput, ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error)
	PutMethod(context.Context, *apigateway.PutMethodInput, ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	DeleteMethod(context.Context, *apigateway.DeleteMethodInput, ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error)
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

type TaggingAPI interface {
	GetResources(context.Context, *resourcegroupstaggingapi.GetResourcesInput, ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

type Clients struct {
	APIGateway APIGatewayAPI
	Routing    RoutingAPI
	Tagging    TaggingAPI
	Dynamo     awsports.DynamoAPI
	CFN        cfn.StacksAPI
	Region     string
}

type apiGateway struct {
	ns         account.Namespace
	ensurePart func(ctx context.Context, tier environment.Tier, part, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error)
	open       func(context.Context) (Clients, error)

	mu        sync.Mutex
	delete    *Deletion
	clients   *Clients
	leaseWait func(context.Context, time.Duration) error
}

func New(ns account.Namespace, ensurePart func(ctx context.Context, tier environment.Tier, part, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error), open func(context.Context) (Clients, error)) edge.Edge {
	front := newAPIGateway(ns, open)
	front.ensurePart = ensurePart
	return front
}

func newAPIGateway(ns account.Namespace, open func(context.Context) (Clients, error)) *apiGateway {
	return &apiGateway{ns: ns, open: open, delete: NewDeletion(), leaseWait: waitFor}
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
			Tagging:    resourcegroupstaggingapi.NewFromConfig(awscfg),
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
		Supported: []edge.Need{edge.NeedStreaming, edge.NeedRevalidation},
	}
}

func (p *apiGateway) Hooks() edge.Hooks { return edge.Hooks{} }

func CertificateRegion(apiRegion string) string { return apiRegion }

const (
	typeRestAPI    = "AWS::ApiGateway::RestApi"
	typeDomainName = "AWS::ApiGateway::DomainName"
)

func (p *apiGateway) PlanProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
	changes := []edge.PlanChange{{
		Kind:   typeRestAPI,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
		Reason: restAPIsReason(scope.Tier),
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

func restAPIsReason(tier environment.Tier) string {
	const paced = "; API Gateway deletes at most one REST API every 30 seconds per account, so a project with many previews takes a while"
	if tier == environment.TierPreview {
		return "every preview API this project is served through, and the host rules routing to them" + paced
	}
	return "the production API and every preview API this project is served through, and the host rules routing to them" + paced
}

func (p *apiGateway) PlanPreviewWildcardRemoval(wildcard string) (edge.PlanGroup, edge.PlanGroup) {
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
	return removed, p.PlanSharedPreviewRemoval()
}

func (p *apiGateway) PlanSharedPreviewRemoval() edge.PlanGroup {
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

func knownTier(tier environment.Tier) error {
	if tier != environment.TierProduction && tier != environment.TierPreview {
		return fmt.Errorf("the %q edge does not know the tier %q", Kind, tier)
	}
	return nil
}

func (p *apiGateway) bootstrap(ctx context.Context, c Clients, tier environment.Tier) (account.Deployed, error) {
	if err := knownTier(tier); err != nil {
		return account.Deployed{}, err
	}
	if tier == environment.TierPreview {
		return account.CheckDeployedPreview(ctx, c.CFN, p.ns)
	}
	return account.CheckDeployed(ctx, c.CFN, p.ns)
}

func (p *apiGateway) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if err := knownTier(tier); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (p *apiGateway) Teardown(ctx context.Context, tier environment.Tier) error {
	if err := knownTier(tier); err != nil {
		return err
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	tagged, err := taggedAPIs(ctx, c, namespaceTags(p.ns, tier))
	if err != nil {
		return err
	}
	var projects []string
	for _, tags := range tagged {
		if project := tags[registry.ProjectTag]; project != "" && !slices.Contains(projects, project) {
			projects = append(projects, project)
		}
	}
	slices.Sort(projects)
	if len(projects) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %q edge still fronts %d project(s) of tier %s with a REST API of their own: %s. Run `%s` in each of them first, then take this bootstrap down",
		Kind, len(projects), tier, strings.Join(projects, ", "), "ocel destroy "+string(tier))
}

func (p *apiGateway) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	if spec.Slug == "" {
		return nil, fmt.Errorf("the %q edge fronts a project by slug; this stack names none", Kind)
	}
	deployed, err := p.bootstrap(ctx, c, spec.Tier)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, fmt.Errorf("the %s bootstrap is not installed, so the %q edge has no state table to keep %s's deployments in", spec.Tier, Kind, spec.Slug)
	}
	role, err := requireInvokeRole(p.ns, deployed, spec.Tier)
	if err != nil {
		return nil, err
	}

	var own private
	if err := prior.Private.Into(&own); err != nil {
		return nil, err
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	own.StateTable = deployed.StateTable
	own.EdgeBucket = deployed.Buckets[naming.StorageEdge]
	own.Role = role
	own.Region = c.Region

	s := &stack{p: p, state: next, own: own}
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

func (p *apiGateway) ProjectOwner(slug string, tier environment.Tier) string {
	return apiName(p.ns, slug, tier, "")
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
	for _, mapping := range mappings {
		id := aws.ToString(mapping.RestApiId)
		if id == "" {
			continue
		}
		api, err := c.APIGateway.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(id)})
		if isNotFound(err) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("read REST API %s, which %s is mapped to: %w", id, hostname, err)
		}
		if name := aws.ToString(api.Name); name != "" {
			return name, nil
		}
		return id, nil
	}
	return "", nil
}

func apiName(ns account.Namespace, slug string, tier environment.Tier, pointer string) string {
	if p := router.ResolvePointer(pointer); p != router.DefaultPointer {
		return surface.Name(ns, slug, tier, p)
	}
	return surface.Name(ns, slug, tier)
}

func accountOf(roleARN string) string {
	parts := strings.Split(roleARN, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}
