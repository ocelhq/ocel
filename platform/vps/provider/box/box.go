package box

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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
	ForgetNetwork(ctx context.Context, class providerkit.Class, project string) error
	Promote(ctx context.Context, class providerkit.Class, project, app, coordinate string) error
	Serving(ctx context.Context, key host.RouteKey) (string, error)
	Release(ctx context.Context, rel host.Release, report providerkit.Reporter) error
	UnroutePointer(ctx context.Context, owner, pointer string) error
	UnrouteSurface(ctx context.Context, owner string) error
	Claims(ctx context.Context) ([]host.HostClaim, error)
	Pins() []host.Pin
	RouteBy(hostname string) string
	ClaimHosts(ctx context.Context, claims []host.HostClaim) error
	DisclaimHost(ctx context.Context, hostname, owner string) error
	DisclaimPointer(ctx context.Context, owner, pointer string) error
	DisclaimSurface(ctx context.Context, owner string) error
	PreviewEntry(ctx context.Context) (string, error)
	InstallPreviewEntry(ctx context.Context, base string) error
	RemovePreviewEntry(ctx context.Context, base string) error
}

type Origins func(ctx context.Context, project string, class providerkit.Class) error

type Edge struct {
	machine Machine
	origins Origins
	records providerkit.RecordStore
	scope   string
}

var _ edge.Edge = (*Edge)(nil)

func New(machine Machine, origins Origins, records providerkit.RecordStore, scope string) *Edge {
	return &Edge{machine: machine, origins: origins, records: records, scope: scope}
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
	return live.Surface(slug, string(class))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge needs a project slug; this stack has none", Kind)
	}
	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	next.PreviewBase = declaredPreviewBase(spec)
	s := &stack{e: e, state: next}
	if err := s.ledger().EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func declaredPreviewBase(spec edge.StackSpec) string {
	if spec.Class != edge.ClassPreview {
		return ""
	}
	for _, hostname := range spec.Domains {
		if base, wild := strings.CutPrefix(hostname, "*."); wild {
			return base
		}
	}
	return ""
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{e: e, state: state}, nil
}

func (e *Edge) DomainOwner(ctx context.Context, hostname string) (string, error) {
	if base, wild := strings.CutPrefix(hostname, "*."); wild {
		held, err := e.machine.PreviewEntry(ctx)
		if err != nil || held != base {
			return "", err
		}
		return edge.PreviewEntryOwner, nil
	}
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

func (e *Edge) ProjectOwner(slug string, class edge.Class) string {
	return Surface(slug, class)
}

func (e *Edge) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	if err := host.PreviewBaseUsable(spec.BaseDomain); err != nil {
		return "", err
	}
	address, err := e.machine.Address(ctx)
	if err != nil {
		return "", err
	}
	if err := e.machine.InstallPreviewEntry(ctx, spec.BaseDomain); err != nil {
		return "", err
	}
	if route := e.machine.RouteBy(edge.PreviewWildcard(spec.BaseDomain)); route != "" && spec.Warn != nil {
		spec.Warn(route)
	}
	return address, nil
}

func (e *Edge) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	return e.machine.RemovePreviewEntry(ctx, baseDomain)
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
				Reason: "proxy route"},
			e.certificateKept(hostname))
	}
	if len(group.Changes) == 0 {
		group.Action = edge.PlanKeep
		group.Reason = "no hostname claimed"
	}
	return []edge.PlanGroup{group}
}

func (e *Edge) certificateKept(hostname string) edge.PlanChange {
	if path := host.Covering(e.machine.Pins(), hostname); path != "" {
		return edge.PlanChange{Kind: CertificateKind, Name: certs.PinHandle(path), Action: edge.PlanKeep,
			Reason: "pinned at " + path + "; you renew it"}
	}
	reason := "the proxy renews it"
	if e.machine.RouteBy(hostname) != "" {
		reason = "held by your proxy"
	}
	return edge.PlanChange{Kind: CertificateKind, Name: certs.ProxyHandle(hostname), Action: edge.PlanKeep, Reason: reason}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	removed = edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: RouteKind, Name: wildcard, Action: edge.PlanDelete,
			Reason: "proxy route",
		}},
	}
	return removed, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "shared catch-all, kept",
	}
}
