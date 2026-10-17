package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	s3store "github.com/ocelhq/ocel/platform/s3"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/bucket"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
	forwards := make([]provider.PortForward, 0, len(req.Bindings))
	for _, binding := range req.Bindings {
		local, stop, err := p.containers.Forward(ctx, binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort])
		if err != nil {
			for _, opened := range forwards {
				opened.Close()
			}
			return nil, err
		}
		forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: local, Close: stop})
	}
	return forwards, nil
}

const buildSessions = "build"

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		ref := provider.StackRef{Project: req.Slug, Tier: req.Tier, Name: naming.InfraStack(req.Env)}
		store, stopForward, err := p.buckets().OpenRoot(ctx, ref)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: s3store.New(s3store.Config{
				Objects:      store.Client(),
				Internal:     store.Presigner(),
				External:     func(context.Context) (s3store.PresignAPI, string) { return store.Presigner(), store.Endpoint },
				Callbacks:    s3store.HTTPPoster{},
				PostPolicies: true,
				Sessions:     bucket.SessionsPrefix(ref, buildSessions),
				Granted:      s3store.NewGrantsByOwnName(buildproxy.ListBucketNames(grant)...),
			})}})
		}
		proxy, err := bindingproxy.Serve(served, req.ReportFailure)
		if err != nil {
			stopForward()
			return provider.BindingProxy{}, err
		}
		sessions := make([]provider.BindingSession, 0, len(proxy.Sessions))
		for _, session := range proxy.Sessions {
			sessions = append(sessions, provider.BindingSession{Grantee: session.Grantee, SessionToken: session.Token})
		}
		return provider.BindingProxy{Address: proxy.Address, Sessions: sessions, Close: func() {
			_ = proxy.Close()
			stopForward()
		}}, nil
	})
}
