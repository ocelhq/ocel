package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

const Kind edge.Kind = "alb"

const BaselineCost = "about $18 a month plus Premium-tier egress"

type Deps struct {
	KeyValues keyvalue.Store
	Stacks    Stacks
	Routes    Routes
	Entries   Entries
	Pins      pin.Pins
	WarmURL   func(ctx context.Context, url, address string) error
	Project   string
	Region    string
	Shielded  bool
}

type Edge struct{ deps Deps }

func (e *Edge) loadBalancerTarget(tier environment.Tier) Target {
	return Target{Tier: tier, Shielded: e.deps.Shielded}
}

func New(deps Deps) *Edge { return &Edge{deps: deps} }

func (e *Edge) Kind() edge.Kind { return Kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:             []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming},
		InvalidatesByCacheTag: true,
		ShieldsOrigin:         true,
		CredentialScope:       e.deps.Project,
	}
}

func (e *Edge) Hooks() edge.Hooks {
	return edge.Hooks{
		CheckBootstrapInstalled:       e.bootstrapInstalled,
		ListBoundHostnames:            e.boundHostnames,
		DescribeCredentialPermissions: e.credentialPermissions,
	}
}

func (e *Edge) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if tier == "" {
		return edge.BootstrapOutput{}, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge provisions one load balancer per tier, and this bootstrap names none", Kind)
	}
	balancer, err := e.raise(ctx, tier, progress.Discard())
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustExternal, Values: map[string]string{
		outputAddress:        balancer.Address,
		outputCertificateMap: balancer.CertificateMap,
		outputURLMap:         balancer.URLMap,
		outputNotFound:       balancer.NotFound,
	}}, nil
}

func (e *Edge) raise(ctx context.Context, tier environment.Tier, progress progress.Log) (LoadBalancer, error) {
	var preview previewEntry
	if tier == environment.TierPreview {
		recorded, err := e.recordedPreview(ctx)
		if err != nil {
			return LoadBalancer{}, err
		}
		if recorded.Shielded == e.deps.Shielded {
			preview = recorded
		}
	}
	return e.raiseServing(ctx, tier, preview, progress)
}

func (e *Edge) raiseServing(ctx context.Context, tier environment.Tier, preview previewEntry, progress progress.Log) (LoadBalancer, error) {
	spec := loadBalancerSpec{
		Region:  e.deps.Region,
		Names:   loadBalancerNames(tier, e.deps.Shielded),
		Preview: preview,
	}
	if e.deps.Shielded {
		trusted, err := e.ensureTrusted(ctx, tier)
		if err != nil {
			return LoadBalancer{}, err
		}
		spec.ClientCAs = trusted
	}
	outputs, err := e.deps.Stacks.Up(ctx, e.loadBalancerTarget(tier), loadBalancerProgram(spec), progress)
	if err != nil {
		return LoadBalancer{}, err
	}
	balancer := loadBalancerOf(outputs)
	balancer.Shielded = e.deps.Shielded
	if !balancer.provisioned() {
		return LoadBalancer{}, fmt.Errorf(
			"provision the %s load balancer for tier %s: it reported %+v, and a hostname is bound by writing into its certificate map and its url map",
			Kind, tier, balancer)
	}
	return balancer, nil
}

func (e *Edge) bootstrapInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
	if err != nil {
		return false, err
	}
	return loadBalancerOf(outputs).provisioned(), nil
}

func (e *Edge) boundHostnames(ctx context.Context, tier environment.Tier) ([]string, error) {
	if e.deps.Entries == nil {
		return nil, nil
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
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
			Kind, tier, strings.Join(bound, ", "))
	}
	if err := e.deps.Stacks.Destroy(ctx, e.loadBalancerTarget(tier), progress.Discard()); err != nil {
		return err
	}
	if !e.deps.Shielded {
		return nil
	}
	return e.forgetTrust(ctx, tier)
}

type edgeRecord struct {
	LoadBalancer   LoadBalancer        `json:"loadBalancer,omitzero"`
	Hosts          map[string]Host     `json:"hosts,omitempty"`
	DeploymentTags map[string]pin.Tags `json:"deploymentTags,omitempty"`
	Pointers       pin.Pointers        `json:"pointers,omitempty"`

	Served map[string]servedRelease `json:"served,omitempty"`
}

type Host struct {
	App         string `json:"app,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	Service     string `json:"service,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Backend     string `json:"backend,omitempty"`
	Pointer     string `json:"pointer,omitempty"`
}

func Surface(slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, "ocel", string(Kind), naming.Sanitize(slug), string(tier))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge serves a project by slug, and this stack names none", Kind)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(spec.Tier))
	if err != nil {
		return nil, err
	}
	balancer := loadBalancerOf(outputs)
	if !balancer.provisioned() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: the %q edge serves every project in a tier from one that the bootstrap raises, at %s. Run `ocel bootstrap` for this tier first",
			Kind, spec.Tier, Kind, BaselineCost)
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	s := &stack{e: e, state: next}
	if err := s.adopt(balancer); err != nil {
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

func (e *Edge) claim(tier environment.Tier, hostname string) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), "domains", hostname)
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
		entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.claim(tier, hostname))
		if err != nil {
			return "", fmt.Errorf("read what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		if len(entry.Value) == 0 {
			continue
		}
		var claimed claim
		if err := json.Unmarshal(entry.Value, &claimed); err != nil {
			return "", fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		return claimed.Owner, nil
	}
	return "", nil
}

type claim struct {
	Owner string `json:"owner"`
}

func (e *Edge) ProjectOwner(slug string, tier environment.Tier) string { return Surface(slug, tier) }

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	changes := make([]edge.PlanChange, 0, len(scope.Hostnames)*3+1)
	for _, hostname := range scope.Hostnames {
		changes = append(changes,
			edge.PlanChange{Kind: "compute.URLMap host rule", Name: hostname, Action: edge.PlanDelete},
			edge.PlanChange{Kind: "compute.BackendService", Name: backendName(scope.Slug, scope.Tier, hostname), Action: edge.PlanDelete},
			edge.PlanChange{Kind: "certificatemanager.CertificateMapEntry", Name: entryName(scope.Slug, scope.Tier, hostname), Action: edge.PlanDelete},
		)
	}
	changes = append(changes, edge.PlanChange{
		Kind: "compute.RegionNetworkEndpointGroup", Name: BindingStack(scope.Slug, scope.Tier), Action: edge.PlanDelete,
	})
	return []edge.PlanGroup{
		{
			Kind:    edge.EdgeGroupKind,
			Name:    edge.EdgeGroupName(Kind),
			Action:  edge.PlanDelete,
			Changes: changes,
		},
		{
			Kind:   edge.EdgeGroupKind,
			Name:   edge.EdgeGroupName(Kind) + "/load-balancer",
			Action: edge.PlanKeep,
			Reason: "the load balancer this project was balancing by is one per bootstrap tier and every other project in the tier is answered by it, " +
				"so it stays provisioned and keeps costing " + BaselineCost + "; `ocel bootstrap remove` is what takes it down",
		},
	}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	base := strings.TrimPrefix(wildcard, "*.")
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{
			{Kind: "certificatemanager.CertificateMapEntry", Name: previewEntryName(base), Action: edge.PlanDelete},
		},
	}, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind) + "/load-balancer",
		Action: edge.PlanKeep,
		Reason: "previews are answered by the same load balancer production is, one per bootstrap tier at " + BaselineCost + ", " +
			"so releasing a wildcard takes its certificate and leaves the balancer in place",
	}
}

func ledgerFor(store keyvalue.Store, tier environment.Tier, slug string) *ledger.Ledger {
	return ledger.New(store, tier, slug)
}

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)
