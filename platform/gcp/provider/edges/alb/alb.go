package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Deps struct {
	Namespace string
	KeyValues records.Store
	Routes    Routes
	Entries   Entries
	Pins      pin.Pins
	WarmURL   func(ctx context.Context, url, address, cookie string) error
	Project   string
	Region    string

	Cipher    seal.Cipher
	ProbeGate func(ctx context.Context, hostname, address string) (bool, error)

	WaitBeforeRetry    func(ctx context.Context, attempt int) bool
	ReadAssetsBucket   func(ctx context.Context, tier environment.Tier) (albresources.AssetsBucket, error)
	CopyImmutableFiles func(ctx context.Context, tier environment.Tier, from, to albresources.StaticRoutes) error

	Engine Engine
}

type Engine struct {
	Stacks        Stacks
	Images        PluginImages
	GatePlugin    []byte
	FrontPart     FrontPart
	EvaluateFront func(ctx context.Context, tier environment.Tier) (transform.Result, error)
	ReadApps      func(ctx context.Context, tier environment.Tier, slug string) (map[string]transform.Result, error)
}

type FrontPart struct {
	Claim   func(ctx context.Context, tier environment.Tier, user string) error
	Release func(ctx context.Context, tier environment.Tier, user string) error
}

type Edge struct{ deps Deps }

func (e *Edge) loadBalancerStack(tier environment.Tier) albresources.Stack {
	return albresources.Stack{Tier: tier}
}

func New(deps Deps) *Edge { return &Edge{deps: deps} }

func (e *Edge) Kind() edge.Kind { return albresources.Kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:             []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming, edge.NeedRevalidation},
		InvalidatesByCacheTag: true,
		CachesResponses:       true,
		CacheControlHeader:    "cdn-cache-control",
		Gates:                 true,
		ChecksSignedPasswords: true,
		ShieldsOrigin:         true,
		CredentialScope:       e.deps.Project,
	}
}

func (e *Edge) Hooks() edge.Hooks {
	return edge.Hooks{
		CheckBootstrapInstalled:       e.bootstrapInstalled,
		ListBoundHostnames:            e.boundHostnames,
		DescribeCredentialPermissions: e.credentialPermissions,
		PublishGate:                   e.publishGate,
	}
}

func (e *Edge) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if tier == "" {
		return edge.BootstrapOutput{}, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge provisions one load balancer per tier, and this bootstrap names none", albresources.Kind)
	}
	if _, err := e.raise(ctx, tier, "", progress.Discard()); err != nil {
		return edge.BootstrapOutput{}, err
	}
	if err := e.publishGates(ctx, tier, gateProof{}, progress.Discard()); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (e *Edge) raise(ctx context.Context, tier environment.Tier, project string, progress progress.Log) (LoadBalancer, error) {
	var preview albresources.PreviewEntry
	if tier == environment.TierPreview {
		recorded, err := e.recordedPreview(ctx)
		if err != nil {
			return LoadBalancer{}, err
		}
		preview = recorded
	}
	return e.raiseServing(ctx, tier, preview, project, progress)
}

func (e *Edge) raiseServing(ctx context.Context, tier environment.Tier, preview albresources.PreviewEntry, project string, progress progress.Log) (LoadBalancer, error) {
	spec := albresources.LoadBalancerSpec{
		Namespace: e.deps.Namespace,
		Region:    e.deps.Region,
		Names:     albresources.LoadBalancerNames(e.deps.Namespace, tier),
		Tier:      tier,
		Preview:   preview,
	}
	assets, err := e.deps.ReadAssetsBucket(ctx, tier)
	if err != nil {
		return LoadBalancer{}, err
	}
	spec.Assets = &assets
	patches, _, err := e.readFrontPatches(ctx, tier, project)
	if err != nil {
		return LoadBalancer{}, err
	}
	spec.Patches = patches.result()
	outputs, err := e.raiseFront(ctx, spec, progress)
	if err != nil {
		return LoadBalancer{}, err
	}
	if err := e.recordFront(ctx, tier, patches); err != nil {
		return LoadBalancer{}, err
	}
	balancer := loadBalancerOf(outputs)
	if !balancer.provisioned() {
		return LoadBalancer{}, fmt.Errorf(
			"provision the %s load balancer for tier %s: it reported %+v, and a hostname is bound by writing into its certificate map and its url map",
			albresources.Kind, tier, balancer)
	}
	return balancer, nil
}

func (e *Edge) readProvisionedLoadBalancer(ctx context.Context, tier environment.Tier) (LoadBalancer, error) {
	outputs, err := e.deps.Engine.Stacks.Outputs(ctx, e.loadBalancerStack(tier))
	if err != nil {
		return LoadBalancer{}, err
	}
	balancer := loadBalancerOf(outputs)
	if !balancer.provisioned() {
		return LoadBalancer{}, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: run `ocel bootstrap` for this tier first", albresources.Kind, tier)
	}
	return balancer, nil
}

func (e *Edge) bootstrapInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	outputs, err := e.deps.Engine.Stacks.Outputs(ctx, e.loadBalancerStack(tier))
	if err != nil {
		return false, err
	}
	return loadBalancerOf(outputs).provisioned(), nil
}

func (e *Edge) boundHostnames(ctx context.Context, tier environment.Tier) ([]string, error) {
	if e.deps.Entries == nil {
		return nil, nil
	}
	outputs, err := e.deps.Engine.Stacks.Outputs(ctx, e.loadBalancerStack(tier))
	if err != nil {
		return nil, err
	}
	balancer := loadBalancerOf(outputs)
	if balancer.CertificateMap == "" {
		return nil, nil
	}
	return e.deps.Entries.Entered(ctx, balancer.CertificateMap)
}

func (e *Edge) Teardown(ctx context.Context, tier environment.Tier) error {
	bound, err := e.boundHostnames(ctx, tier)
	if err != nil {
		return err
	}
	if len(bound) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer of tier %s still serves %s, and Google will not delete a certificate map that has entries: "+
				"release those hostnames with `ocel domain remove` in the projects that bound them, then take this bootstrap down",
			albresources.Kind, tier, strings.Join(bound, ", "))
	}
	if err := e.removeGate(ctx, tier); err != nil {
		return err
	}
	if err := e.deps.Engine.Stacks.Destroy(ctx, e.loadBalancerStack(tier), progress.Discard()); err != nil {
		return err
	}
	return e.forgetFront(ctx, tier)
}

type edgeRecord struct {
	LoadBalancer   LoadBalancer        `json:"loadBalancer,omitzero"`
	Hosts          map[string]Host     `json:"hosts,omitempty"`
	DeploymentTags map[string]pin.Tags `json:"deploymentTags,omitempty"`
	Pointers       pin.Pointers        `json:"pointers,omitempty"`
	TagsToRemove   []RevisionTag       `json:"tagsToRemove,omitempty"`

	Served map[string]servedRelease `json:"served,omitempty"`

	Patches map[string]recordedPatches `json:"patches,omitempty"`
}

func (r edgeRecord) appResults() map[string]transform.Result {
	results := make(map[string]transform.Result, len(r.Patches))
	for app, recorded := range r.Patches {
		results[app] = recorded.result()
	}
	return results
}

type RevisionTag struct {
	Service string `json:"service"`
	Tag     string `json:"tag"`
}

type Host struct {
	App          string `json:"app,omitempty"`
	Certificate  string `json:"certificate,omitempty"`
	Service      string `json:"service,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Backend      string `json:"backend,omitempty"`
	Pointer      string `json:"pointer,omitempty"`
	StaticRoutes string `json:"staticRoutes,omitempty"`
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge serves a project by slug, and this stack names none", albresources.Kind)
	}
	outputs, err := e.deps.Engine.Stacks.Outputs(ctx, e.loadBalancerStack(spec.Tier))
	if err != nil {
		return nil, err
	}
	balancer := loadBalancerOf(outputs)
	if !balancer.provisioned() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: the %q edge serves every project in a tier from one, at %s, which a deploy adds before it reaches the edge. Deploy again",
			albresources.Kind, spec.Tier, albresources.Kind, albresources.BaselineCost)
	}
	said := sayingTo(spec.Say)
	repatched, raised, err := e.repatchFront(ctx, spec.Tier, spec.Slug, said)
	if err != nil {
		return nil, err
	}
	if raised {
		balancer = repatched
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	s := &stack{e: e, state: next}
	if err := s.adopt(balancer); err != nil {
		return nil, err
	}
	if err := s.applyAppPatches(ctx, said); err != nil {
		return nil, err
	}
	if err := s.syncGate(ctx, spec); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{e: e, state: state}
	if err := s.state.Private.Into(&s.recorded); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) claim(tier environment.Tier, hostname string) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(albresources.Kind), "domains", hostname)
}

func (e *Edge) DomainOwner(ctx context.Context, hostname string) (string, error) {
	if hostname == "" {
		return "", nil
	}
	if base, wild := strings.CutPrefix(hostname, "*."); wild {
		preview, err := e.recordedPreview(ctx)
		if err != nil || preview.BaseDomain != base {
			return "", err
		}
		return edge.PreviewEntryOwner, nil
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, e.claim(tier, hostname))
		if err != nil {
			return "", fmt.Errorf("read what serves %s on the %s edge: %w", hostname, albresources.Kind, err)
		}
		if len(entry.Value) == 0 {
			continue
		}
		var claimed claim
		if err := json.Unmarshal(entry.Value, &claimed); err != nil {
			return "", fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, albresources.Kind, err)
		}
		return claimed.Owner, nil
	}
	return "", nil
}

type claim struct {
	Owner string `json:"owner"`
}

func (e *Edge) ProjectOwner(_ context.Context, slug string, tier environment.Tier) (string, error) {
	return Surface(e.deps.Namespace, slug, tier), nil
}

func (e *Edge) PlanProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	changes := make([]edge.PlanChange, 0, len(scope.Hostnames)*3+1)
	for _, hostname := range scope.Hostnames {
		backend, err := albresources.BackendName(e.deps.Namespace, scope.Slug, scope.Tier, hostname)
		if err != nil {
			return nil, err
		}
		entry, err := albresources.EntryName(e.deps.Namespace, scope.Slug, scope.Tier, hostname)
		if err != nil {
			return nil, err
		}
		changes = append(changes,
			edge.PlanChange{Kind: "compute.URLMap host rule", Name: hostname, Action: edge.PlanDelete},
			edge.PlanChange{Kind: "compute.BackendService", Name: backend, Action: edge.PlanDelete},
			edge.PlanChange{Kind: "certificatemanager.CertificateMapEntry", Name: entry, Action: edge.PlanDelete},
		)
	}
	changes = append(changes, edge.PlanChange{
		Kind: "compute.RegionNetworkEndpointGroup", Name: albresources.BindingStack(scope.Slug, scope.Tier), Action: edge.PlanDelete,
	})
	return []edge.PlanGroup{
		{
			Kind:    edge.EdgeGroupKind,
			Name:    edge.EdgeGroupName(albresources.Kind),
			Action:  edge.PlanDelete,
			Changes: changes,
		},
		{
			Kind:   edge.EdgeGroupKind,
			Name:   edge.EdgeGroupName(albresources.Kind) + "/load-balancer",
			Action: edge.PlanKeep,
			Reason: "the load balancer this project was balancing by is one per tier and costs " + albresources.BaselineCost + ": " +
				"it stays while another project in the tier is served through it or a hostname is bound to it, and is taken down with the last project otherwise",
		},
	}, nil
}

func (e *Edge) PlanPreviewWildcardRemoval(wildcard string) (removed, kept edge.PlanGroup, err error) {
	entry, err := albresources.PreviewEntryName(e.deps.Namespace, strings.TrimPrefix(wildcard, "*."))
	if err != nil {
		return edge.PlanGroup{}, edge.PlanGroup{}, err
	}
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(albresources.Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{
			{Kind: "certificatemanager.CertificateMapEntry", Name: entry, Action: edge.PlanDelete},
		},
	}, e.PlanSharedPreviewRemoval(), nil
}

func (e *Edge) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(albresources.Kind) + "/load-balancer",
		Action: edge.PlanKeep,
		Reason: "previews are answered by the same load balancer production is, one per bootstrap tier at " + albresources.BaselineCost + ", " +
			"so releasing a wildcard takes its certificate and leaves the balancer in place",
	}
}

func ledgerFor(store records.Store, tier environment.Tier, slug string) *ledger.Ledger {
	return ledger.New(store, tier, slug)
}

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)
