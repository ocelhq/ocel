package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const buildSessions = "build"

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		ref := provider.StackRef{Project: req.Slug, Tier: req.Tier, Name: naming.InfraStack(req.Env)}
		container := p.stores.shape()
		if container == nil {
			shaped := storeContainer(resources.ProvisionRequest{Ref: storeRef(ref)})
			container = &shaped
		}
		root, err := p.stores.once(container.Name, func() (storeCredential, error) {
			return p.storeCredential(ctx, ref, container.Name)
		})
		if err != nil {
			return provider.BindingProxy{}, err
		}
		local, stopForward, err := p.host.ForwardToContainer(ctx, container.Name, storePort)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		store := s3store.Store{
			Endpoint:        "http://" + local,
			Region:          storeRegion,
			AccessKeyID:     storeAccessKey,
			SecretAccessKey: root.secret,
			PathStyle:       true,
		}
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: s3store.New(s3store.Config{
				Objects:      store.Client(),
				Internal:     store.Presigner(),
				External:     func(context.Context) (s3store.PresignAPI, string) { return store.Presigner(), store.Endpoint },
				Callbacks:    s3store.HTTPPoster{},
				PostPolicies: true,
				Sessions:     s3store.SessionsBucket() + "/" + storeRef(ref).Name.String() + "/" + buildSessions,
				Granted:      buildproxy.ListBucketNames(grant),
			})}})
		}
		proxy, err := buildproxy.ServeGrants(served, req.ReportFailure)
		if err != nil {
			stopForward()
			return provider.BindingProxy{}, err
		}
		closeProxy := proxy.Close
		proxy.Close = func() {
			closeProxy()
			stopForward()
		}
		return proxy, nil
	})
}
