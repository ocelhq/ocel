package gcp

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	forwardOpenAttempts = 6
	identityTokenLife   = 30 * time.Minute
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
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
	opened, err := p.newBastion(c).forwardPorts(ctx, req.Tier, targets, progress, req.ReportFailure)
	if err != nil {
		return nil, err
	}
	forwards := make([]provider.PortForward, len(opened))
	for i, forward := range opened {
		forwards[i] = provider.PortForward{Binding: req.Bindings[i].Name, LocalAddress: forward.Address(), Close: forward.Close}
	}
	return forwards, nil
}

func (p *Provider) newBastion(c *gcloud.Clients) bastion {
	return bastion{
		clients:        c,
		deployAndRoute: p.services().DeployAndRoute,
		pushBinary:     p.pushBinary,
		tearDown:       p.services().TearDown,
		grantInvoker:   p.grantInvoker,
		prove:          ports.ProveIdentity,
		open:           relay.OpenForward,
		waited:         googleretry.Waited,
	}
}

func (b bastion) forwardPorts(ctx context.Context, tier environment.Tier, targets []string, progress progress.Log, reportFailure func(error)) ([]*relay.Forward, error) {
	tokens := &identityTokens{prove: b.prove, audience: b.audience(tier), now: time.Now}
	if _, err := tokens.mintOrReuse(ctx); err != nil {
		return nil, err
	}
	url, err := b.provision(ctx, tier, progress)
	if err != nil {
		return nil, err
	}
	forwards := make([]*relay.Forward, 0, len(targets))
	for _, target := range targets {
		forward, err := b.openForward(ctx, relay.Link{URL: url, Target: target, Token: tokens.mintOrReuse, Warn: gcloud.EnsureProgress(progress).Warn, ReportFailure: reportFailure})
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

func (t *identityTokens) mintOrReuse(ctx context.Context) (string, error) {
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

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		c, err := p.openClients(ctx)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		store, err := bucket.Open(ctx, c.Endpoint)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			messages := make([]*bindingsv1.Binding, 0, len(grant.Bindings))
			for _, binding := range grant.Bindings {
				message, err := provider.BindingMessage(binding)
				if err != nil {
					return provider.BindingProxy{}, err
				}
				messages = append(messages, message)
			}
			records, err := s3store.NewStaticRecords(messages...)
			if err != nil {
				return provider.BindingProxy{}, err
			}
			buckets, err := bucket.NewDispatch(ctx, store, records, s3store.HTTPPoster{})
			if err != nil {
				return provider.BindingProxy{}, err
			}
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: buckets}})
		}
		proxy, err := bindingproxy.Serve(served, req.ReportFailure)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		sessions := make([]provider.BindingSession, 0, len(proxy.Sessions))
		for _, session := range proxy.Sessions {
			sessions = append(sessions, provider.BindingSession{Grantee: session.Grantee, SessionToken: session.Token})
		}
		return provider.BindingProxy{Address: proxy.Address, Sessions: sessions, Close: func() { _ = proxy.Close() }}, nil
	})
}
