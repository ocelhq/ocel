package cloudfront

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	edgeHeaderValue = string(edge.KindNative)

	cacheKeyHeader = "x-ocel-cache-key"

	cacheTagHeader = "cache-tag"

	namespace = "ocel"

	allViewerExceptHostPolicyID = "b689b0a8-53d0-40ab-baf2-68738e2966ac"

	stackKeyDistribution  = "distributionId"
	stackKeyStateTable    = "stateTable"
	stackKeyAssetBucket   = "assetBucket"
	stackKeyRegion        = "region"
	stackKeyFunction      = "functionArn"
	stackKeyKeyValueStore = "keyValueStoreArn"
	stackKeyCachePolicy   = "cachePolicyId"
	stackKeyHeadersPolicy = "responseHeadersPolicyId"
	stackKeyOAC           = "originAccessControlId"
	stackKeyPreviewBase   = "previewBase"

	kindDistribution         = "distribution"
	kindWildcardDistribution = "wildcard distribution"
)

type CloudFrontAPI interface {
	CreateKeyValueStore(context.Context, *cloudfront.CreateKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error)
	DescribeKeyValueStore(context.Context, *cloudfront.DescribeKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error)
	DeleteKeyValueStore(context.Context, *cloudfront.DeleteKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error)

	CreateFunction(context.Context, *cloudfront.CreateFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error)
	GetFunction(context.Context, *cloudfront.GetFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.GetFunctionOutput, error)
	DescribeFunction(context.Context, *cloudfront.DescribeFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error)
	UpdateFunction(context.Context, *cloudfront.UpdateFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error)
	PublishFunction(context.Context, *cloudfront.PublishFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error)
	DeleteFunction(context.Context, *cloudfront.DeleteFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error)

	ListCachePolicies(context.Context, *cloudfront.ListCachePoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error)
	CreateCachePolicy(context.Context, *cloudfront.CreateCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error)
	DeleteCachePolicy(context.Context, *cloudfront.DeleteCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error)

	ListResponseHeadersPolicies(context.Context, *cloudfront.ListResponseHeadersPoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error)
	CreateResponseHeadersPolicy(context.Context, *cloudfront.CreateResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error)
	DeleteResponseHeadersPolicy(context.Context, *cloudfront.DeleteResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error)

	ListOriginAccessControls(context.Context, *cloudfront.ListOriginAccessControlsInput, ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error)
	CreateOriginAccessControl(context.Context, *cloudfront.CreateOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error)
	DeleteOriginAccessControl(context.Context, *cloudfront.DeleteOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error)

	ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
	CreateDistribution(context.Context, *cloudfront.CreateDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionOutput, error)
	GetDistribution(context.Context, *cloudfront.GetDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error)
	GetDistributionConfig(context.Context, *cloudfront.GetDistributionConfigInput, ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error)
	UpdateDistribution(context.Context, *cloudfront.UpdateDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error)
	DeleteDistribution(context.Context, *cloudfront.DeleteDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error)
}

type KeyValueStoreAPI interface {
	DescribeKeyValueStore(context.Context, *cloudfrontkeyvaluestore.DescribeKeyValueStoreInput, ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.DescribeKeyValueStoreOutput, error)
	GetKey(context.Context, *cloudfrontkeyvaluestore.GetKeyInput, ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.GetKeyOutput, error)
	ListKeys(context.Context, *cloudfrontkeyvaluestore.ListKeysInput, ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.ListKeysOutput, error)
	UpdateKeys(context.Context, *cloudfrontkeyvaluestore.UpdateKeysInput, ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.UpdateKeysOutput, error)
}

type SSMAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type Clients struct {
	CloudFront    CloudFrontAPI
	KeyValueStore KeyValueStoreAPI
	Dynamo        edgeledger.DynamoAPI
	SSM           SSMAPI
	CFN           bootstrap.CFNDescriber
	Region        string
}

type provider struct {
	open   func(context.Context) (Clients, error)
	settle Settler

	mu      sync.Mutex
	clients *Clients
}

var _ edge.Edge = (*provider)(nil)

func New(open func(context.Context) (Clients, error)) edge.Edge {
	return &provider{open: open, settle: NewSettler()}
}

func FromConfig(load func(context.Context) (aws.Config, error)) func(context.Context) (Clients, error) {
	return func(ctx context.Context) (Clients, error) {
		if load == nil {
			return Clients{}, errors.New("the native edge was built without a way to load AWS configuration")
		}
		awscfg, err := load(ctx)
		if err != nil {
			return Clients{}, err
		}
		global, err := sdkconfig.Control(ctx, certs.CloudFrontRegion)
		if err != nil {
			return Clients{}, err
		}
		return Clients{
			CloudFront:    cloudfront.NewFromConfig(global),
			KeyValueStore: cloudfrontkeyvaluestore.NewFromConfig(global),
			Dynamo:        dynamodb.NewFromConfig(awscfg),
			SSM:           ssm.NewFromConfig(awscfg),
			CFN:           cloudformation.NewFromConfig(awscfg),
			Region:        awscfg.Region,
		}, nil
	}
}

func (p *provider) Kind() edge.Kind { return edge.KindNative }

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
		return Clients{}, errors.New("the native edge has no AWS clients; it must be built with the provider's AWS configuration")
	}
	c, err := p.open(ctx)
	if err != nil {
		return Clients{}, fmt.Errorf("open the AWS clients the native edge fronts deployments with: %w", err)
	}
	p.clients = &c
	return c, nil
}

func (p *provider) settler() Settler {
	if p.settle.Attempts == 0 {
		return NewSettler()
	}
	return p.settle
}

func knownClass(class edge.Class) error {
	if class != edge.ClassProduction && class != edge.ClassPreview {
		return fmt.Errorf("the native edge does not know the substrate class %q", class)
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
	if err := knownClass(class); err != nil {
		return edge.BootstrapOutput{}, err
	}
	if _, err := ensureEdgeSet(ctx, c, class); err != nil {
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
	return teardownEdgeSet(ctx, c, class)
}

func (p *provider) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	if spec.Slug == "" {
		return nil, errors.New("the native edge fronts a project by slug; this stack carries none")
	}
	deployed, err := p.substrate(ctx, c, spec.Class)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, fmt.Errorf("the %s substrate is not bootstrapped, so the native edge has no state table to keep %s's deployments in", spec.Class, spec.Slug)
	}
	set, err := findEdgeSet(ctx, c, spec.Class, edgeSet{
		cachePolicy:         prior[stackKeyCachePolicy],
		headersPolicy:       prior[stackKeyHeadersPolicy],
		originAccessControl: prior[stackKeyOAC],
	})
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
	next[stackKeyRegion] = c.Region
	next[stackKeyFunction] = set.functionARN
	next[stackKeyKeyValueStore] = set.keyValueStoreARN
	next[stackKeyCachePolicy] = set.cachePolicy
	next[stackKeyHeadersPolicy] = set.headersPolicy
	next[stackKeyOAC] = set.originAccessControl
	if base := next[edge.StackKeyGlobalPreview]; base != "" {
		next[stackKeyPreviewBase] = base
	}

	s := &stack{p: p, state: next}
	if err := s.ledger(c).EnsureSchema(ctx); err != nil {
		return nil, err
	}
	if spec.PruneOnly {
		return s, nil
	}
	front, err := s.reconcileDistribution(ctx, c)
	if err != nil {
		return nil, err
	}
	next[stackKeyDistribution] = front.id
	next[edge.StackKeyFront] = front.domainName
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
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		owner, found, err := routeOwner(ctx, c, class, hostname)
		if err != nil {
			return "", err
		}
		if found {
			return owner, nil
		}
	}
	summaries, err := listDistributions(ctx, c)
	if err != nil {
		return "", err
	}
	for _, summary := range summaries {
		if summary.aliases[routeKey(hostname)] {
			return summary.comment, nil
		}
	}
	return "", nil
}

func distributionName(slug string, class edge.Class) string {
	return naming.Join(naming.FieldSeparator, namespace, slug, string(class))
}

func keyValueStoreName(class edge.Class) string {
	return setName("routes", class)
}

func functionName(class edge.Class) string {
	return setName("resolver", class)
}

func cachePolicyName(class edge.Class) string {
	return setName("cache", class)
}

func headersPolicyName(class edge.Class) string {
	return setName("headers", class)
}

func originAccessControlName(class edge.Class) string {
	return setName("assets", class)
}

func setName(what string, class edge.Class) string {
	if class == edge.ClassPreview {
		return naming.Join(naming.WordSeparator, namespace, what, string(edge.ClassPreview))
	}
	return naming.Join(naming.WordSeparator, namespace, what)
}

func assetOriginDomain(bucket, region string) string {
	if bucket == "" || region == "" {
		return ""
	}
	return bucket + ".s3." + region + ".amazonaws.com"
}

func assetOriginPath(prefix string) string {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

func originHost(rawURL string) string {
	host := rawURL
	if _, rest, found := strings.Cut(host, "://"); found {
		host = rest
	}
	host, _, _ = strings.Cut(host, "/")
	return strings.TrimSuffix(host, ":443")
}

func pointerOr(pointer string) string {
	if pointer == "" {
		return edge.DefaultPointer
	}
	return pointer
}

func ptr[T any](value T) *T { return &value }

func quantity[T any](items []T) *int32 {
	n := int32(len(items))
	return &n
}
