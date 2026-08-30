package box

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const Kind edge.Kind = "box"

const (
	RouteKind       = "proxy:route"
	CertificateKind = "proxy:certificate"
)

type Machine interface {
	Address(ctx context.Context) (string, error)
	HoldsImage(ctx context.Context, coordinate string) (bool, error)
	StandUp(ctx context.Context, spec host.Container) error
	Promote(ctx context.Context, class providerkit.Class, app, coordinate string) error
	Serving(ctx context.Context, key host.RouteKey) (string, error)
	Release(ctx context.Context, rel host.Release, report providerkit.Reporter) error
	UnroutePointer(ctx context.Context, owner, pointer string) error
	UnrouteSurface(ctx context.Context, owner string) error
	Claims(ctx context.Context) ([]host.HostClaim, error)
	Pins() []host.Pin
	ClaimHost(ctx context.Context, claim host.HostClaim) error
	DisclaimHost(ctx context.Context, hostname, owner string) error
	DisclaimSurface(ctx context.Context, owner string) error
}

type Edge struct {
	machine Machine
	records providerkit.RecordStore
	scope   string
}

var _ edge.Edge = (*Edge)(nil)

func New(machine Machine, records providerkit.RecordStore, scope string) *Edge {
	return &Edge{machine: machine, records: records, scope: scope}
}

func (e *Edge) Kind() edge.Kind { return Kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{CredentialScope: e.scope}
}

var supported = []edge.Need{edge.NeedStreaming}

func (e *Edge) Supported() []edge.Need { return slices.Clone(supported) }

func (e *Edge) FlipBound() edge.FlipBound { return edge.FlipBound{} }

func (e *Edge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (e *Edge) Teardown(context.Context, edge.Class) error { return nil }

func Surface(slug string, class edge.Class) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), string(class))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge serves a project by slug, and this stack carries none", Kind)
	}
	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	s := &stack{e: e, state: next}
	if err := s.ledger().EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{e: e, state: state}, nil
}

func (e *Edge) DomainOwner(ctx context.Context, hostname string) (string, error) {
	claims, err := e.machine.Claims(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(claims, func(claim host.HostClaim) bool { return claim.Hostname == hostname })
	if at < 0 {
		return "", nil
	}
	return claims[at].Owner, nil
}

func (e *Edge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", e.unbuilt("serve a preview wildcard")
}

func (e *Edge) DestroyPreviewWildcard(context.Context, string) error {
	return e.unbuilt("take a preview wildcard down")
}

func (e *Edge) unbuilt(what string) error {
	return providerkit.Refuse(providerkit.CodeNotReady,
		"the %q edge cannot %s yet: a box serves the hostnames it is given one at a time, and the shared preview wildcard it would answer on is not built", Kind, what)
}

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	group := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
	}
	for _, hostname := range scope.Hostnames {
		if hostname == "" {
			continue
		}
		group.Changes = append(group.Changes,
			edge.PlanChange{Kind: RouteKind, Name: hostname, Action: edge.PlanDelete,
				Reason: "the route on this box's proxy claiming " + hostname + " for " + Surface(scope.Slug, scope.Class)},
			e.certificateKept(hostname))
	}
	if len(group.Changes) == 0 {
		group.Action = edge.PlanKeep
		group.Reason = "this project claims no hostname on this box, and the containers it runs are the release surface's rows rather than the edge's"
	}
	return []edge.PlanGroup{group}
}

func (e *Edge) certificateKept(hostname string) edge.PlanChange {
	if path := host.Covering(e.machine.Pins(), hostname); path != "" {
		return edge.PlanChange{Kind: CertificateKind, Name: certs.PinHandle(path), Action: edge.PlanKeep,
			Reason: "the pair you placed at " + path + " and renew, which serves " + hostname + " and every other name it covers: ocel never placed it and removes nothing it did not place"}
	}
	return edge.PlanChange{Kind: CertificateKind, Name: certs.ProxyHandle(hostname), Action: edge.PlanKeep,
		Reason: "the certificate this box's proxy obtained for " + hostname + " and renews, which stays in the proxy's own store: ocel placed no key here so it removes none, and a hostname bound again inside the certificate's life is served off it rather than ordered again"}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	removed = edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: RouteKind, Name: wildcard, Action: edge.PlanDelete,
			Reason: "the route on this box's proxy claiming " + wildcard,
		}},
	}
	return removed, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "the catch-all every unclaimed hostname on this box falls through to is a bootstrap item, and it answers for every project this box serves rather than for this one",
	}
}
