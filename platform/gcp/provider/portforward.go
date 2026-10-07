package gcp

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

const (
	forwardOpenAttempts = 6
	identityTokenLife   = 30 * time.Minute
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
	if len(req.Bindings) == 0 {
		return nil, nil
	}
	targets := make([]string, 0, len(req.Bindings))
	for _, binding := range req.Bindings {
		host, port := binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort]
		if host == "" || port == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid, "%s names no host and port, so no bastion can forward to it", binding.Name)
		}
		targets = append(targets, net.JoinHostPort(host, port))
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	opened, err := p.openBastion(c).forwardPorts(ctx, req.Tier, targets, nil)
	if err != nil {
		return nil, err
	}
	forwards := make([]provider.PortForward, len(opened))
	for i, forward := range opened {
		forwards[i] = provider.PortForward{Binding: req.Bindings[i].Name, LocalAddress: forward.Address(), Close: forward.Close}
	}
	return forwards, nil
}

func (p *Provider) openBastion(c *clients) bastion {
	return bastion{
		clients:        c,
		deployAndRoute: p.deployAndRoute,
		pushBinary:     p.pushBinary,
		tearDown:       p.tearDown,
		grantInvoker:   p.grantInvoker,
		prove:          ports.ProveIdentity,
		open:           relay.OpenForward,
		waited:         waited,
	}
}

func (b bastion) forwardPorts(ctx context.Context, tier environment.Tier, targets []string, progress progress.Log) ([]*relay.Forward, error) {
	tokens := &identityTokens{prove: b.prove, audience: b.clients.Bastion(tier), now: time.Now}
	if _, err := tokens.mint(ctx); err != nil {
		return nil, err
	}
	url, err := b.provision(ctx, tier, progress)
	if err != nil {
		return nil, err
	}
	forwards := make([]*relay.Forward, 0, len(targets))
	for _, target := range targets {
		forward, err := b.openForward(ctx, relay.Link{URL: url, Target: target, Token: tokens.mint})
		if err != nil {
			for _, opened := range forwards {
				opened.Close()
			}
			return nil, err
		}
		forwards = append(forwards, forward)
	}
	return forwards, nil
}

func (b bastion) openForward(ctx context.Context, link relay.Link) (*relay.Forward, error) {
	for attempt := 1; ; attempt++ {
		forward, err := b.open(ctx, link)
		if err == nil {
			return forward, nil
		}
		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeNotReady || attempt == forwardOpenAttempts {
			return nil, err
		}
		if !b.waited(ctx, attempt) {
			return nil, ctx.Err()
		}
	}
}

type identityTokens struct {
	prove    func(ctx context.Context, audience string) (envsource.IdentityProof, error)
	audience string
	now      func() time.Time

	mutex  sync.Mutex
	token  string
	minted time.Time
}

func (t *identityTokens) mint(ctx context.Context) (string, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.token != "" && t.now().Sub(t.minted) < identityTokenLife {
		return t.token, nil
	}
	proof, err := t.prove(ctx, t.audience)
	if err != nil {
		return "", err
	}
	t.token, t.minted = proof.IDToken, t.now()
	return t.token, nil
}
