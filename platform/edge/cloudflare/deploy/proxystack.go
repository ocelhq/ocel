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

func (x *Proxy) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	accountID, err := x.p.readTierAccount(ctx, spec.Tier, "reconcile the Cloudflare stack")
	if err != nil {
		return nil, err
	}
	next, err := x.p.retireFronting(ctx, accountID, prior)
	if err != nil {
		return nil, err
	}
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

type proxyStack struct {
	x     *Proxy
	state edge.StackState
}

func (s *proxyStack) State() edge.StackState { return s.state }

func (s *proxyStack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return errors.New("binding a domain to the Cloudflare proxy needs a hostname")
	}
	if binding.Origin == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %q edge forwards %s to an origin, and this binding names none: it answers no hostname itself",
			Kind, binding.Hostname)
	}
	names, err := s.x.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return err
	}
	accountID, err := s.x.p.readTierAccount(ctx, s.state.Tier, fmt.Sprintf("forward %s through Cloudflare", binding.Hostname))
	if err != nil {
		return err
	}
	zone, err := s.x.p.resolveHostnameZone(ctx, accountID, binding.Hostname)
	if err != nil {
		return err
	}
	return s.x.p.bindOrigin(ctx, &s.state, zone, owner, binding)
}

func (s *proxyStack) UnbindDomain(ctx context.Context, hostname string) error {
	if hostname == "" {
		return errors.New("unbinding a domain from the Cloudflare proxy needs a hostname")
	}
	names, err := s.x.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	accountID, err := s.x.p.readTierAccount(ctx, s.state.Tier, fmt.Sprintf("stop forwarding %s through Cloudflare", hostname))
	if err != nil {
		return err
	}
	return s.unbindDomain(ctx, names, accountID, hostname)
}

func (s *proxyStack) unbindDomain(ctx context.Context, names originNames, accountID, hostname string) error {
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return err
	}
	zone, err := s.x.p.resolveHostnameZone(ctx, accountID, hostname)
	if err != nil {
		return err
	}
	return s.x.p.unbindOrigin(ctx, &s.state, zone, owner, hostname)
}

func (s *proxyStack) Destroy(ctx context.Context) error {
	accountID, err := s.x.p.readTierAccount(ctx, s.state.Tier, "destroy the Cloudflare stack")
	if err != nil {
		return err
	}
	names, err := s.x.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range s.state.Bound {
		if err := s.unbindDomain(ctx, names, accountID, hostname); err != nil {
			errs = append(errs, fmt.Errorf("stop forwarding %q: %w", hostname, err))
		}
	}
	return errors.Join(errs...)
}

var _ edge.EdgeStack = (*proxyStack)(nil)
