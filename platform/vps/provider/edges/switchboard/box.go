package switchboard

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

const (
	RouteKind       = "proxy:route"
	CertificateKind = "proxy:certificate"
)

type Machine interface {
	Address(ctx context.Context) (string, error)
	EnsureImage(ctx context.Context, project, app, imageRef string) (bool, error)
	RunContainer(ctx context.Context, spec host.Container) (host.Run, error)
	ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error
	Promote(ctx context.Context, tier environment.Tier, project, app, imageRef string) error
	RollOut(ctx context.Context, rollout host.Rollout, progress progress.Log) error
	Discard(ctx context.Context, rollout host.Rollout) string
	Pins() *certs.Pins
	RouteBy(hostname string) string
	RefuseUnshielded(ctx context.Context, hostname string) error
}

type TunnelContainer interface {
	Reserve(ctx context.Context, front edge.Kind, connector edge.TunnelConnector) (boxapi.Tunnel, error)
	Run(ctx context.Context, tunnel boxapi.Tunnel, connector edge.TunnelConnector, token func(context.Context) (string, error)) error
	Release(ctx context.Context) ([]boxapi.Tunnel, error)
	Stop(ctx context.Context, tunnel boxapi.Tunnel) error
	Forget(ctx context.Context, name string) error
}

type Routes interface {
	Serving(ctx context.Context, key boxapi.RouteKey) (string, error)
	UnroutePointer(ctx context.Context, owner, pointer string) error
	UnrouteSurface(ctx context.Context, owner string) error
	Claims(ctx context.Context) ([]boxapi.Claim, error)
	ClaimHosts(ctx context.Context, claims []boxapi.Claim) error
	PutShield(ctx context.Context, shield boxapi.Shield) (boxapi.Shield, error)
	RemoveShield(ctx context.Context, hostname, owner string) error
	PutGate(ctx context.Context, owner string, held *gate.Gate) error
	DisclaimHost(ctx context.Context, hostname, owner string) error
	DisclaimPointer(ctx context.Context, owner, pointer string) error
	DisclaimSurface(ctx context.Context, owner string) error
	PreviewEntry(ctx context.Context) (string, error)
	InstallPreviewEntry(ctx context.Context, base string) error
	RemovePreviewEntry(ctx context.Context, base string) error
	ClaimThrough(ctx context.Context, tunnelName string, claims []boxapi.Claim) error
	InstallPreviewEntryThrough(ctx context.Context, base, tunnelName string) error
}

type Origins func(ctx context.Context, project string, tier environment.Tier) error

type RecordedGate func(ctx context.Context, tier environment.Tier, project string) (*gate.Gate, error)

type Edge struct {
	machine  Machine
	routes   Routes
	tunnel   TunnelContainer
	origins  Origins
	scope    string
	tunnels  Tunnels
	readGate RecordedGate
}

var _ edge.Edge = (*Edge)(nil)

func New(machine Machine, routes Routes, tunnel TunnelContainer, origins Origins, scope string, tunnels Tunnels, readGate RecordedGate) *Edge {
	return &Edge{machine: machine, routes: routes, tunnel: tunnel, origins: origins, scope: scope, tunnels: tunnels, readGate: readGate}
}

func (e *Edge) Kind() edge.Kind { return edge.None }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:       []edge.Need{edge.NeedStreaming, edge.NeedRevalidation},
		Gates:           true,
		CredentialScope: e.scope,
	}
}

func (e *Edge) Hooks() edge.Hooks {
	return edge.Hooks{PublishGate: e.publishGate}
}

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func Surface(slug string, tier environment.Tier) string {
	return boxapi.Surface(slug, string(tier))
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
	if err := e.putGate(ctx, spec.Slug, spec.Tier, spec.Gate); err != nil {
		return nil, err
	}
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
		entry, err := e.routes.PreviewEntry(ctx)
		if err != nil || entry != base {
			return "", err
		}
		return edge.PreviewEntryOwner, nil
	}
	claims, err := e.routes.Claims(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(claims, func(claim boxapi.Claim) bool { return claim.Hostname == hostname })
	if at < 0 {
		return "", nil
	}
	return claims[at].Owner, nil
}

func (e *Edge) ProjectOwner(slug string, tier environment.Tier) string {
	return Surface(slug, tier)
}

func (e *Edge) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	if err := routing.RefuseInvalidPreviewBase(spec.BaseDomain); err != nil {
		return "", err
	}
	address, err := e.machine.Address(ctx)
	if err != nil {
		return "", err
	}
	if err := e.routes.InstallPreviewEntry(ctx, spec.BaseDomain); err != nil {
		return "", err
	}
	if route := e.machine.RouteBy(edge.PreviewWildcard(spec.BaseDomain)); route != "" && spec.Warn != nil {
		spec.Warn(route)
	}
	return address, nil
}

func (e *Edge) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	return e.routes.RemovePreviewEntry(ctx, baseDomain)
}

func (e *Edge) PlanProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
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
	if path := e.machine.Pins().Covering(hostname); path != "" {
		return edge.PlanChange{Kind: CertificateKind, Name: certs.PinHandle(path), Action: edge.PlanKeep,
			Reason: "pinned at " + path + "; you renew it"}
	}
	reason := "the proxy renews it"
	if e.machine.RouteBy(hostname) != "" {
		reason = "your proxy serves it"
	}
	return edge.PlanChange{Kind: CertificateKind, Name: certs.ProxyHandle(hostname), Action: edge.PlanKeep, Reason: reason}
}

func (e *Edge) PlanPreviewWildcardRemoval(wildcard string) (removed, kept edge.PlanGroup) {
	removed = edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: RouteKind, Name: wildcard, Action: edge.PlanDelete,
			Reason: "proxy route",
		}},
	}
	return removed, e.PlanSharedPreviewRemoval()
}

func (e *Edge) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "shared catch-all, kept",
	}
}
