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
			"the %q edge forwards %s to an origin, and this binding names none: it answers no hostname itself",
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

var _ edge.EdgeStack = (*proxyStack)(nil)
