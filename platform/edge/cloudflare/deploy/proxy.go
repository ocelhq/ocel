package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	kindDNSRecord         = "Cloudflare::DNSRecord"
	kindClientCertificate = "Cloudflare::OriginPullCertificate"
)

type Proxy struct{ p *cloudflare }

func NewProxy(namespace string) *Proxy { return &Proxy{p: newCloudflare(namespace)} }

func (x *Proxy) Kind() edge.Kind { return Kind }

func (x *Proxy) Facts() edge.Facts {
	return edge.Facts{
		Supported:       []edge.Need{edge.NeedStreaming},
		ProxiesRecords:  true,
		ShieldsOrigin:   true,
		CredentialScope: x.p.accountID(),
	}
}

func (x *Proxy) Hooks() edge.Hooks {
	return edge.Hooks{
		VerifyCredentials:             x.p.verifyCredentials,
		DescribeCredentialPermissions: proxyCredentialPermissions,
		EnsureClientCertificate:       x.p.ensureClientCertificate,
		PurgeHostnames:                x.p.purgeHostnames,
	}
}

func (x *Proxy) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (x *Proxy) Teardown(context.Context, environment.Tier) error { return nil }

func (x *Proxy) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	for _, domain := range spec.Domains {
		if strings.HasPrefix(domain, "*.") {
			return nil, previewsUnserved(domain)
		}
	}
	next := prior
	next.Slug, next.Tier = spec.Slug, spec.Tier
	return &proxyStack{x: x, state: next}, nil
}

func previewsUnserved(wildcard string) error {
	// TODO(#1396): forward a preview wildcard to the router's preview entry once a proxied *.preview.<base> has an edge certificate (Advanced Certificate Manager) to answer it with.
	return refusal.Refuse(refusal.CodeInvalid,
		"the %q edge forwards production hostnames to this provider's router, and does not forward the preview wildcard %s yet: "+
			"leave `edge` out to serve previews from the origin", Kind, wildcard)
}

func (x *Proxy) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &proxyStack{x: x, state: state}, nil
}

func (x *Proxy) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	return "", previewsUnserved(edge.PreviewWildcard(spec.BaseDomain))
}

func (x *Proxy) DestroyPreviewWildcard(context.Context, string) error { return nil }

func (x *Proxy) DomainOwner(ctx context.Context, hostname string) (string, error) {
	accountID := x.p.accountID()
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to read what Cloudflare forwards", envAccountID)
	}
	zoneID, _, err := x.p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return "", err
	}
	return x.p.readForwardedOwner(ctx, zoneID, hostname)
}

func (x *Proxy) ProjectOwner(slug string, tier environment.Tier) string {
	return forwardingOwner(x.p.namespace, slug, tier)
}

func (x *Proxy) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	group := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
	}
	for _, hostname := range scope.Hostnames {
		group.Changes = append(group.Changes, edge.PlanChange{
			Kind: kindDNSRecord, Name: hostname, Action: edge.PlanDelete,
			Reason: "the proxied record that forwards it to the origin",
		})
	}
	if len(group.Changes) == 0 {
		group.Action = edge.PlanKeep
		group.Reason = "no hostname forwarded"
	}
	return []edge.PlanGroup{group, {
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind) + "/zone",
		Action: edge.PlanKeep,
		Reason: "the client certificate each zone presents to origins is shared by every project forwarded through that zone",
		Changes: []edge.PlanChange{{
			Kind: kindClientCertificate, Name: "zone-level authenticated origin pulls", Action: edge.PlanKeep,
		}},
	}}
}

func (x *Proxy) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: kindDNSRecord, Name: wildcard, Action: edge.PlanDelete,
			Reason: "the proxied record that would forward it to the origin",
		}},
	}, x.SharedPreviewRemoval()
}

func (x *Proxy) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "the proxy provisions nothing shared by previews",
	}
}

type proxyStack struct {
	x     *Proxy
	state edge.StackState
}

func (s *proxyStack) State() edge.StackState { return s.state }

func (s *proxyStack) owner() string { return s.x.ProjectOwner(s.state.Slug, s.state.Tier) }

func (s *proxyStack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return errors.New("binding a domain to the Cloudflare proxy needs a hostname")
	}
	if binding.Origin == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %q edge forwards %s to an origin, and this binding names none: the router this provider pairs it with claims the hostname and names where it answers",
			Kind, binding.Hostname)
	}
	return s.x.p.bindOrigin(ctx, &s.state, s.owner(), binding)
}

func (s *proxyStack) UnbindDomain(ctx context.Context, hostname string) error {
	if hostname == "" {
		return errors.New("unbinding a domain from the Cloudflare proxy needs a hostname")
	}
	return s.x.p.unbindOrigin(ctx, &s.state, s.owner(), hostname)
}

func (s *proxyStack) Destroy(ctx context.Context) error {
	var errs []error
	for _, hostname := range s.state.Bound {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("stop forwarding %q: %w", hostname, err))
		}
	}
	return errors.Join(errs...)
}

var (
	_ edge.Edge      = (*Proxy)(nil)
	_ edge.EdgeStack = (*proxyStack)(nil)
)
