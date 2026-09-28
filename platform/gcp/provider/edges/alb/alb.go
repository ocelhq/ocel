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
	Project   string
	Region    string
}

type Edge struct{ deps Deps }

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
	front, err := e.raise(ctx, tier, progress.DiscardProgress())
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustExternal, Values: map[string]string{
		outputAddress:        front.Address,
		outputCertificateMap: front.CertificateMap,
		outputURLMap:         front.URLMap,
		outputNotFound:       front.NotFound,
	}}, nil
}

func (e *Edge) raise(ctx context.Context, tier environment.Tier, progress progress.Progress) (Front, error) {
	var preview previewEntry
	if tier == environment.TierPreview {
		recorded, err := e.recordedPreview(ctx)
		if err != nil {
			return Front{}, err
		}
		preview = recorded
	}
	return e.raiseServing(ctx, tier, preview, progress)
}

func (e *Edge) raiseServing(ctx context.Context, tier environment.Tier, preview previewEntry, progress progress.Progress) (Front, error) {
	names := frontNames(tier)
	outputs, err := e.deps.Stacks.Up(ctx, Target{Tier: tier}, frontProgram(frontSpec{
		Region:  e.deps.Region,
		Names:   names,
		Preview: preview,
	}), progress)
	if err != nil {
		return Front{}, err
	}
	front := frontOf(outputs)
	if !front.provisioned() {
		return Front{}, fmt.Errorf(
			"provision the %s load balancer for tier %s: it reported %+v, and a hostname is bound by writing into its certificate map and its url map",
			Kind, tier, front)
	}
	return front, nil
}

func (e *Edge) bootstrapInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Tier: tier})
	if err != nil {
		return false, err
	}
	return frontOf(outputs).provisioned(), nil
}

func (e *Edge) boundHostnames(ctx context.Context, tier environment.Tier) ([]string, error) {
	if e.deps.Entries == nil {
		return nil, nil
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Tier: tier})
	if err != nil {
		return nil, err
	}
	front := frontOf(outputs)
	if front.CertificateMap == "" {
		return nil, nil
	}
	return e.deps.Entries.Entered(ctx, front.CertificateMap)
}

func (e *Edge) Teardown(ctx context.Context, tier environment.Tier) error {
	bound, err := e.boundHostnames(ctx, tier)
	if err != nil {
		return err
	}
	if len(bound) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s front of tier %s still serves %s, and Google will not delete a certificate map that has entries: "+
				"release those hostnames with `ocel domain remove` in the projects that bound them, then take this bootstrap down",
			Kind, tier, strings.Join(bound, ", "))
	}
	return e.deps.Stacks.Destroy(ctx, Target{Tier: tier}, progress.DiscardProgress())
}

type edgeRecord struct {
	Front Front           `json:"front,omitzero"`
	Hosts map[string]Host `json:"hosts,omitempty"`
}

type Host struct {
	App         string `json:"app,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	Service     string `json:"service,omitempty"`
	Backend     string `json:"backend,omitempty"`
}

func Surface(slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, "ocel", string(Kind), naming.Sanitize(slug), string(tier))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge serves a project by slug, and this stack names none", Kind)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Tier: spec.Tier})
	if err != nil {
		return nil, err
	}
	front := frontOf(outputs)
	if !front.provisioned() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: the %q edge fronts every project in a tier from one that the bootstrap raises, at %s. Run `ocel bootstrap` for this tier first",
			Kind, spec.Tier, Kind, BaselineCost)
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	s := &stack{e: e, state: next}
	if err := s.adopt(front); err != nil {
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
			Name:   edge.EdgeGroupName(Kind) + "/front",
			Action: edge.PlanKeep,
			Reason: "the load balancer this project was fronted by is one per bootstrap tier and every other project in the tier is answered by it, " +
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
			{Kind: "compute.URLMap host rule", Name: wildcard, Action: edge.PlanDelete},
			{Kind: "compute.BackendService", Name: previewBackendName(base), Action: edge.PlanDelete},
			{Kind: "compute.RegionNetworkEndpointGroup", Name: previewNEGName(base), Action: edge.PlanDelete},
			{Kind: "certificatemanager.CertificateMapEntry", Name: previewEntryName(base), Action: edge.PlanDelete},
		},
	}, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind) + "/front",
		Action: edge.PlanKeep,
		Reason: "previews are answered by the same load balancer production is, one per bootstrap tier at " + BaselineCost + ", " +
			"so releasing a wildcard takes its host rule and leaves the front in place",
	}
}

func ledgerFor(store keyvalue.Store, tier environment.Tier, slug string) *ledger.Ledger {
	return ledger.New(store, tier, slug)
}

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)
