package apigateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	EdgeHeader = "x-ocel-edge"

	edgeHeaderValue = string(edge.KindNone)

	stageName = "live"

	entryVariable  = "entry"
	assetsVariable = "assets"

	unsetVariable = "unset"

	apiNamespace = "ocel"

	stackKeyAPI         = "restApiId"
	stackKeyStateTable  = "stateTable"
	stackKeyAssetBucket = "assetBucket"
	stackKeyRole        = "invokeRole"
	stackKeyRegion      = "region"
)

type APIGatewayAPI interface {
	GetRestApis(context.Context, *apigateway.GetRestApisInput, ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	CreateRestApi(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	DeleteRestApi(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	GetResources(context.Context, *apigateway.GetResourcesInput, ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	CreateResource(context.Context, *apigateway.CreateResourceInput, ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	PutMethod(context.Context, *apigateway.PutMethodInput, ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	PutIntegration(context.Context, *apigateway.PutIntegrationInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
	PutMethodResponse(context.Context, *apigateway.PutMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.PutMethodResponseOutput, error)
	PutIntegrationResponse(context.Context, *apigateway.PutIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationResponseOutput, error)
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

type IAMAPI interface {
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	CreateRole(context.Context, *iam.CreateRoleInput, ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	PutRolePolicy(context.Context, *iam.PutRolePolicyInput, ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRolePolicy(context.Context, *iam.DeleteRolePolicyInput, ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	DeleteRole(context.Context, *iam.DeleteRoleInput, ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

type Clients struct {
	APIGateway APIGatewayAPI
	Routing    RoutingAPI
	Dynamo     edgeledger.DynamoAPI
	IAM        IAMAPI
	CFN        bootstrap.CFNDescriber
	Region     string
}

type provider struct {
	open func(context.Context) (Clients, error)

	mu      sync.Mutex
	delete  *Deleter
	clients *Clients
}

var _ edge.Edge = (*provider)(nil)

func New(open func(context.Context) (Clients, error)) edge.Edge {
	return &provider{open: open, delete: NewDeleter()}
}

func FromConfig(load func(context.Context) (aws.Config, error)) func(context.Context) (Clients, error) {
	return func(ctx context.Context) (Clients, error) {
		if load == nil {
			return Clients{}, errors.New("the none edge was built without a way to load AWS configuration")
		}
		awscfg, err := load(ctx)
		if err != nil {
			return Clients{}, err
		}
		return Clients{
			APIGateway: apigateway.NewFromConfig(awscfg),
			Routing:    apigatewayv2.NewFromConfig(awscfg),
			Dynamo:     dynamodb.NewFromConfig(awscfg),
			IAM:        iam.NewFromConfig(awscfg),
			CFN:        cloudformation.NewFromConfig(awscfg),
			Region:     awscfg.Region,
		}, nil
	}
}

func (p *provider) deleter() *Deleter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.delete == nil {
		p.delete = NewDeleter()
	}
	return p.delete
}

func (p *provider) Kind() edge.Kind { return edge.KindNone }

func (p *provider) Supports(need edge.Need) bool {
	return edge.CapabilitiesOf(p.Kind()).Supports(need)
}

func (p *provider) Supported() []edge.Need {
	return edge.CapabilitiesOf(p.Kind()).Supported()
}

func (p *provider) FlipBound() edge.FlipBound {
	return edge.CapabilitiesOf(p.Kind()).FlipBound()
}

func (p *provider) clientsFor(ctx context.Context) (Clients, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients != nil {
		return *p.clients, nil
	}
	if p.open == nil {
		return Clients{}, errors.New("the none edge has no AWS clients; it must be built with the provider's AWS configuration")
	}
	c, err := p.open(ctx)
	if err != nil {
		return Clients{}, fmt.Errorf("open the AWS clients the none edge fronts deployments with: %w", err)
	}
	p.clients = &c
	return c, nil
}

func knownClass(class edge.Class) error {
	if class != edge.ClassProduction && class != edge.ClassPreview {
		return fmt.Errorf("the none edge does not know the substrate class %q", class)
	}
	return nil
}

func (p *provider) substrate(ctx context.Context, c Clients, class edge.Class) (bootstrap.Deployed, error) {
	if err := knownClass(class); err != nil {
		return bootstrap.Deployed{}, err
	}
	if class == edge.ClassPreview {
		return bootstrap.CheckDeployedPreview(ctx, c.CFN)
	}
	return bootstrap.CheckDeployed(ctx, c.CFN)
}

func (p *provider) Bootstrap(ctx context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	deployed, err := p.substrate(ctx, c, class)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	if _, err := ensureInvokeRole(ctx, c, class, deployed.AssetBucket); err != nil {
		return edge.BootstrapOutput{}, err
	}
	if _, err := ensureNotFoundAPI(ctx, c, class); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (p *provider) Teardown(ctx context.Context, class edge.Class) error {
	if err := knownClass(class); err != nil {
		return err
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	id, found, err := findAPI(ctx, c, notFoundAPIName(class))
	switch {
	case err != nil:
		errs = append(errs, err)
	case found:
		if err := p.deleter().drain(ctx, c, []string{id}); err != nil {
			errs = append(errs, err)
		}
	}
	if err := deleteInvokeRole(ctx, c, class); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p *provider) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	if spec.Slug == "" {
		return nil, errors.New("the none edge fronts a project by slug; this stack carries none")
	}
	deployed, err := p.substrate(ctx, c, spec.Class)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, fmt.Errorf("the %s substrate is not bootstrapped, so the none edge has no state table to keep %s's deployments in", spec.Class, spec.Slug)
	}
	role, err := requireInvokeRole(ctx, c, spec.Class)
	if err != nil {
		return nil, err
	}

	next := maps.Clone(prior)
	if next == nil {
		next = edge.StackState{}
	}
	next[edge.StackKeySlug] = spec.Slug
	next[edge.StackKeyClass] = string(spec.Class)
	next[stackKeyStateTable] = deployed.StateTable
	next[stackKeyAssetBucket] = deployed.AssetBucket
	next[stackKeyRole] = role
	next[stackKeyRegion] = c.Region

	s := &stack{p: p, state: next}
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
	next[stackKeyAPI] = id
	if err := s.settleDomainFronts(ctx, c, spec.Warn); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *provider) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{p: p, state: maps.Clone(state)}, nil
}

func (p *provider) DomainOwner(ctx context.Context, hostname string) (string, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return "", err
	}
	held, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{DomainName: aws.String(hostname)})
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the API Gateway domain name for %s: %w", hostname, err)
	}
	if held.RoutingMode == agtypes.RoutingModeRoutingRuleOnly {
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

func apiName(slug string, class edge.Class, pointer string) string {
	fields := []string{apiNamespace, slug, string(class)}
	if p := pointerOr(pointer); p != edge.DefaultPointer {
		fields = append(fields, p)
	}
	return naming.Join(naming.FieldSeparator, fields...)
}

func notFoundAPIName(class edge.Class) string {
	return naming.Join(naming.WordSeparator, apiNamespace, "not-found", string(class))
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
