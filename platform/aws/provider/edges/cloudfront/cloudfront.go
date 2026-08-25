package cloudfront

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Kind edge.Kind = "cloudfront"

const propagationBound = 5 * time.Second

const (
	namespace = "ocel"

	listPageCeiling = 200

	allViewerExceptHostPolicyID = "b689b0a8-53d0-40ab-baf2-68738e2966ac"

	kindDistribution         = "distribution"
	kindWildcardDistribution = "wildcard distribution"
)

type CloudFrontAPI interface {
	DescribeKeyValueStore(context.Context, *cloudfront.DescribeKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error)

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
	Dynamo        awsports.DynamoAPI
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
			return Clients{}, fmt.Errorf("the %q edge was built without a way to load AWS configuration", Kind)
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

func (p *provider) Kind() edge.Kind { return Kind }

func (p *provider) Facts() edge.Facts {
	return edge.Facts{InvalidatesByCacheTag: true}
}

var supported = []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming}

func (p *provider) Supported() []edge.Need {
	return slices.Clone(supported)
}

func (p *provider) FlipBound() edge.FlipBound {
	return edge.FlipBound{Typical: propagationBound}
}

func (p *provider) CertificateRegion(string) string { return certs.CloudFrontRegion }

const distributionDeleteReason = "CloudFront only deletes a disabled distribution once the disable has reached every edge"

const (
	typeDistribution  = "AWS::CloudFront::Distribution"
	typeKeyValueStore = "AWS::CloudFront::KeyValueStore"
)

func (p *provider) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	var changes []edge.PlanChange
	if scope.Front != "" {
		changes = append(changes, edge.PlanChange{
			Kind:   typeDistribution,
			Name:   scope.Front,
			Action: edge.PlanDisableThenDelete,
			Reason: distributionDeleteReason,
			Slow:   true,
		})
	}
	for _, hostname := range scope.Hostnames {
		changes = append(changes, edge.PlanChange{
			Kind:   typeKeyValueStore,
			Name:   hostname,
			Action: edge.PlanDelete,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(Kind),
		Action:  edge.PlanDelete,
		Changes: changes,
	}}
}

func (p *provider) PreviewWildcardRemovals(wildcard string) (edge.PlanGroup, edge.PlanGroup) {
	removed := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind:   typeDistribution,
			Name:   wildcard,
			Action: edge.PlanDisableThenDelete,
			Reason: distributionDeleteReason,
			Slow:   true,
		}},
	}
	return removed, p.SharedPreviewRemoval()
}

func (p *provider) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: every project's routes are read from the preview resolver function and its key-value store",
	}
}

func (p *provider) clientsFor(ctx context.Context) (Clients, error) {
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

func (p *provider) settler() Settler {
	if p.settle.Attempts == 0 {
		return NewSettler()
	}
	return p.settle
}

func knownClass(class edge.Class) error {
	if class != edge.ClassProduction && class != edge.ClassPreview {
		return fmt.Errorf("the %q edge does not know the class %q", Kind, class)
	}
	return nil
}

func (p *provider) bootstrap(ctx context.Context, c Clients, class edge.Class) (bootstrap.Deployed, error) {
	if err := knownClass(class); err != nil {
		return bootstrap.Deployed{}, err
	}
	if class == edge.ClassPreview {
		return bootstrap.CheckDeployedPreview(ctx, c.CFN)
	}
	return bootstrap.CheckDeployed(ctx, c.CFN)
}

func (p *provider) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	if err := knownClass(class); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (p *provider) Teardown(_ context.Context, class edge.Class) error {
	return knownClass(class)
}

func (p *provider) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	c, err := p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	if spec.Slug == "" {
		return nil, fmt.Errorf("the %q edge fronts a project by slug; this stack carries none", Kind)
	}
	deployed, err := p.bootstrap(ctx, c, spec.Class)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, fmt.Errorf("the %s bootstrap is not standing, so the %q edge has no state table to keep %s's deployments in. Run `%s` against this account, then deploy again", spec.Class, Kind, spec.Slug, providerkit.BootstrapCommand(spec.Class))
	}
	var own private
	if err := prior.Adapter.Into(&own); err != nil {
		return nil, err
	}
	set, err := edgeSetOf(deployed, spec.Class)
	if err != nil {
		return nil, err
	}

	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	own.StateTable = deployed.StateTable
	own.AssetBucket = deployed.AssetBucket
	own.Region = c.Region
	own.Function = set.functionARN
	own.KeyValueStore = set.keyValueStoreARN
	own.CachePolicy = set.cachePolicy
	own.HeadersPolicy = set.headersPolicy
	own.OriginAccessControl = set.originAccessControl
	if base := next.GlobalPreview; base != "" {
		own.PreviewBase = base
	}

	s := &stack{p: p, state: next, own: own}
	if err := s.ledger(c).EnsureSchema(ctx); err != nil {
		return nil, err
	}
	if spec.PruneOnly {
		return s, nil
	}
	if _, err := s.reconcileDistribution(ctx, c); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *provider) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{p: p, state: state}
	if err := state.Adapter.Into(&s.own); err != nil {
		return nil, err
	}
	return s, nil
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
