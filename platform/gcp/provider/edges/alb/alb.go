package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

const Kind edge.Kind = "alb"

const StandingCost = "about $18 a month plus Premium-tier egress"

type Deps struct {
	Records providerkit.RecordStore
	Stacks  Stacks
	Routes  Routes
	Entries Entries
	Pins    pin.Pins
	Project string
	Region  string
}

type Edge struct{ deps Deps }

func New(deps Deps) *Edge { return &Edge{deps: deps} }

func (e *Edge) Kind() edge.Kind { return Kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{InvalidatesByCacheTag: true, CredentialScope: e.deps.Project}
}

var supported = []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming}

func (e *Edge) Supported() []edge.Need { return slices.Clone(supported) }

func (e *Edge) FlipBound() edge.FlipBound { return edge.FlipBound{} }

func (e *Edge) Bootstrap(ctx context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	if class == "" {
		return edge.BootstrapOutput{}, providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge stands one load balancer up per class, and this bootstrap names none", Kind)
	}
	front, err := e.raise(ctx, class, edge.DiscardReporter())
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

func (e *Edge) raise(ctx context.Context, class edge.Class, report edge.Reporter) (Front, error) {
	names := frontNames(class)
	outputs, err := e.deps.Stacks.Up(ctx, Target{Class: class}, frontProgram(frontSpec{
		Project: e.deps.Project,
		Names:   names,
	}), report)
	if err != nil {
		return Front{}, err
	}
	front := frontOf(outputs)
	if !front.standing() {
		return Front{}, fmt.Errorf(
			"stand the %s load balancer for class %s up: it reported %+v, and a hostname is bound by writing into its certificate map and its url map",
			Kind, class, front)
	}
	return front, nil
}

func (e *Edge) Bound(ctx context.Context, class edge.Class) ([]string, error) {
	if e.deps.Entries == nil {
		return nil, nil
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Class: class})
	if err != nil {
		return nil, err
	}
	front := frontOf(outputs)
	if front.CertificateMap == "" {
		return nil, nil
	}
	return e.deps.Entries.Entered(ctx, front.CertificateMap)
}

func (e *Edge) Teardown(ctx context.Context, class edge.Class) error {
	bound, err := e.Bound(ctx, class)
	if err != nil {
		return err
	}
	if len(bound) > 0 {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the %s front of class %s still serves %s, and Google will not delete a certificate map that holds entries: "+
				"release those hostnames with `ocel domain remove` in the projects that bound them, then take this bootstrap down",
			Kind, class, strings.Join(bound, ", "))
	}
	return e.deps.Stacks.Destroy(ctx, Target{Class: class}, edge.DiscardReporter())
}

type held struct {
	Front Front           `json:"front,omitzero"`
	Hosts map[string]Host `json:"hosts,omitempty"`
}

type Host struct {
	App         string `json:"app,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	Service     string `json:"service,omitempty"`
	Backend     string `json:"backend,omitempty"`
}

func Surface(slug string, class edge.Class) string {
	return naming.Join(naming.FieldSeparator, "ocel", string(Kind), naming.Sanitize(slug), string(class))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge serves a project by slug, and this stack carries none", Kind)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Class: spec.Class})
	if err != nil {
		return nil, err
	}
	front := frontOf(outputs)
	if !front.standing() {
		return nil, providerkit.Refuse(providerkit.CodeNotReady,
			"no %s load balancer stands for class %s: the %q edge fronts every project in a class from one that the bootstrap raises, at %s. Run `ocel bootstrap` for this class first",
			Kind, spec.Class, Kind, StandingCost)
	}
	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	s := &stack{e: e, state: next}
	if err := s.adopt(front); err != nil {
		return nil, err
	}
	if err := s.ledger().EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{e: e, state: state}
	if err := s.state.Adapter.Into(&s.held); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) claim(class edge.Class, hostname string) providerkit.RecordName {
	return append(providerkit.EdgeStacksRecord(class), string(Kind), "domains", hostname)
}

func (e *Edge) DomainOwner(ctx context.Context, hostname string) (string, error) {
	if hostname == "" {
		return "", nil
	}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		record, err := providerkit.Held(ctx, e.deps.Records, e.claim(class, hostname))
		if err != nil {
			return "", fmt.Errorf("read what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		if len(record.Bytes) == 0 {
			continue
		}
		var claimed claim
		if err := json.Unmarshal(record.Bytes, &claimed); err != nil {
			return "", fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		return claimed.Owner, nil
	}
	return "", nil
}

type claim struct {
	Owner string `json:"owner"`
}

func (e *Edge) ProjectOwner(slug string, class edge.Class) string { return Surface(slug, class) }

func (e *Edge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"the %q edge does not route preview hostnames yet: whether one wildcard host rule and one serverless neg with a url mask can resolve a preview's "+
			"slug--pointer--app label in a single dns label is undocumented, and it is being spiked before it is designed. "+
			"Front previews with the cloudflare edge, or reach a preview at the url Cloud Run gives each service under the %q edge",
		Kind, "direct")
}

func (e *Edge) DestroyPreviewWildcard(context.Context, string) error { return nil }

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	changes := make([]edge.PlanChange, 0, len(scope.Hostnames)*3+1)
	for _, hostname := range scope.Hostnames {
		changes = append(changes,
			edge.PlanChange{Kind: "compute.URLMap host rule", Name: hostname, Action: edge.PlanDelete},
			edge.PlanChange{Kind: "compute.BackendService", Name: backendName(scope.Slug, scope.Class, hostname), Action: edge.PlanDelete},
			edge.PlanChange{Kind: "certificatemanager.CertificateMapEntry", Name: entryName(scope.Slug, scope.Class, hostname), Action: edge.PlanDelete},
		)
	}
	changes = append(changes, edge.PlanChange{
		Kind: "compute.RegionNetworkEndpointGroup", Name: BindingStack(scope.Slug, scope.Class), Action: edge.PlanDelete,
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
			Reason: "the load balancer this project was fronted by is one per bootstrap class and every other project in the class is answered by it, " +
				"so it stands and keeps costing " + StandingCost + "; `ocel bootstrap remove` is what takes it down",
		},
	}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{
			{Kind: "compute.URLMap host rule", Name: wildcard, Action: edge.PlanDelete},
			{Kind: "certificatemanager.CertificateMapEntry", Name: naming.Sanitize(wildcard), Action: edge.PlanDelete},
		},
	}, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind) + "/front",
		Action: edge.PlanKeep,
		Reason: "previews are answered by the same load balancer production is, one per bootstrap class at " + StandingCost + ", " +
			"so releasing a wildcard takes its host rule and leaves the front standing",
	}
}

func ledgerFor(records providerkit.RecordStore, class edge.Class, slug string) *kitledger.Ledger {
	return kitledger.New(records, class, slug)
}

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)
