package box

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	RouteKind       = "proxy:route"
	CertificateKind = "proxy:certificate"
)

type Machine interface {
	Address(ctx context.Context) (string, error)
	HasImage(ctx context.Context, imageRef string) (bool, error)
	RunContainer(ctx context.Context, spec host.Container) error
	ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error
	Promote(ctx context.Context, tier environment.Tier, project, app, imageRef string) error
	Serving(ctx context.Context, key host.RouteKey) (string, error)
	Release(ctx context.Context, rel host.Release, progress progress.Log) error
	UnroutePointer(ctx context.Context, owner, pointer string) error
	UnrouteSurface(ctx context.Context, owner string) error
	Claims(ctx context.Context) ([]host.HostClaim, error)
	Pins() []host.Pin
	RouteBy(hostname string) string
	ClaimHosts(ctx context.Context, claims []host.HostClaim) error
	PutShield(ctx context.Context, shield host.Shield) (host.Shield, error)
	RemoveShield(ctx context.Context, hostname, owner string) error
	RefuseUnshielded(ctx context.Context, hostname string) error
	DisclaimHost(ctx context.Context, hostname, owner string) error
	DisclaimPointer(ctx context.Context, owner, pointer string) error
	DisclaimSurface(ctx context.Context, owner string) error
	PreviewEntry(ctx context.Context) (string, error)
	InstallPreviewEntry(ctx context.Context, base string) error
	RemovePreviewEntry(ctx context.Context, base string) error
}

type Origins func(ctx context.Context, project string, tier environment.Tier) error

type Edge struct {
	machine Machine
	origins Origins
	scope   string
}

var _ edge.Edge = (*Edge)(nil)

func New(machine Machine, origins Origins, scope string) *Edge {
	return &Edge{machine: machine, origins: origins, scope: scope}
}

func (e *Edge) Kind() edge.Kind { return edge.None }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:       []edge.Need{edge.NeedStreaming},
		CredentialScope: e.scope,
	}
}

func (e *Edge) Hooks() edge.Hooks { return edge.Hooks{} }

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func Surface(slug string, tier environment.Tier) string {
	return live.Surface(slug, string(tier))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the box serves a project by its slug, and this stack names none")
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	next.PreviewBase = declaredPreviewBase(spec)
	return &stack{e: e, state: next}, nil
}

func declaredPreviewBase(spec edge.StackSpec) string {
	if spec.Tier != environment.TierPreview {
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
		entry, err := e.machine.PreviewEntry(ctx)
		if err != nil || entry != base {
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

func (e *Edge) ProjectOwner(slug string, tier environment.Tier) string {
	return Surface(slug, tier)
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
		Name:   edge.EdgeGroupName(edge.None),
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
		reason = "your proxy serves it"
	}
	return edge.PlanChange{Kind: CertificateKind, Name: certs.ProxyHandle(hostname), Action: edge.PlanKeep, Reason: reason}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	removed = edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
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
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "shared catch-all, kept",
	}
}
