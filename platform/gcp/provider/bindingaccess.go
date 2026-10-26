package gcp

import (
	"context"
	"net"
	"path"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/gcp/provider/bastion"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	s3store "github.com/ocelhq/ocel/platform/s3"
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
	for _, binding := range req.Bindings {
		if err := refuseElsewhere(ctx, c, req.Tier, binding); err != nil {
			return nil, err
		}
	}
	reach, err := network.ReadRange(ctx, c, req.Tier)
	if err != nil {
		return nil, err
	}
	opened, err := bastion.New(c, p.services(), p.pushBinary).ForwardPorts(ctx, req.Tier, reach, targets, progress, req.ReportFailure)
	if err != nil {
		return nil, err
	}
	forwards := make([]provider.PortForward, len(opened))
	for i, forward := range opened {
		forwards[i] = provider.PortForward{Binding: req.Bindings[i].Name, LocalAddress: forward.Address(), Close: forward.Close}
	}
	return forwards, nil
}

func refuseElsewhere(ctx context.Context, c *gcloud.Clients, tier environment.Tier, binding provider.Binding) error {
	on, err := network.ReadEndpointNetwork(ctx, c, tier, binding)
	if err != nil || on == "" || on == c.NetworkPath(tier) {
		return err
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s %s is on the %s network, and the bastion forwards ports only into the %s network of tier %s: reach it from a machine on %s",
		binding.Type, binding.Name, path.Base(on), c.Network(tier), tier, path.Base(on))
}

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		c, err := p.openClients(ctx)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		store, err := bucket.Open(ctx, c.Endpoint, c.QuotaProject())
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
