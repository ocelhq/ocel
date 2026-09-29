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
		ClientCertificates: &edge.ClientCertificateHooks{
			Ensure:  x.p.ensureClientCertificates,
			Present: x.p.presentClientCertificates,
		},
		OriginCertificates: &edge.OriginCertificateHooks{
			Issue:  x.p.issueOriginCertificate,
			Revoke: x.p.revokeOriginCertificate,
		},
		PurgeHostnames: x.p.purgeHostnames,
	}
}

func (x *Proxy) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (x *Proxy) Teardown(context.Context, environment.Tier) error { return nil }

func (x *Proxy) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	next := prior
	next.Slug, next.Tier = spec.Slug, spec.Tier
	next.PreviewBase = ""
	for _, domain := range spec.Domains {
		if base, wild := strings.CutPrefix(domain, "*."); wild && spec.Tier == environment.TierPreview {
			next.PreviewBase = base
		}
	}
	return &proxyStack{x: x, state: next}, nil
}

func (x *Proxy) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &proxyStack{x: x, state: state}, nil
}

func (x *Proxy) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", errors.New("forwarding previews through the Cloudflare proxy needs a base domain")
	}
	if spec.Origin == nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %q edge forwards %s to an origin, and this reconcile names none: the router this provider pairs it with claims the preview entry and names where it answers",
			Kind, wildcard)
	}
	accountID := x.p.accountID()
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to forward %s through Cloudflare", envAccountID, wildcard)
	}
	if _, err := x.p.forwardOrigin(ctx, accountID, edge.PreviewEntryOwner, wildcard, *spec.Origin); err != nil {
		return "", err
	}
	return spec.Origin.Address, nil
}

func (x *Proxy) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	accountID := x.p.accountID()
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to stop forwarding %s through Cloudflare", envAccountID, wildcard)
	}
	zoneID, _, err := x.p.resolveZone(ctx, accountID, baseDomain)
	if err != nil {
		return err
	}
	return x.p.dropForwardRecords(ctx, zoneID, edge.PreviewEntryOwner, wildcard)
}

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
