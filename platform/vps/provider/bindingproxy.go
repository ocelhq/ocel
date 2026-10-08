package vps

import (
	"context"
	"slices"

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
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, bindings []provider.Binding) (bindingproxy.Services, func(), error) {
		var buckets []string
		for _, binding := range bindings {
			if name := binding.Properties[provider.PropertyBucket]; !slices.Contains(buckets, name) {
				buckets = append(buckets, name)
			}
		}
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
			return bindingproxy.Services{}, nil, err
		}
		local, stopForward, err := p.host.ForwardToContainer(ctx, container.Name, storePort)
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		store := s3store.Store{
			Endpoint:        "http://" + local,
			Region:          storeRegion,
			AccessKeyID:     storeAccessKey,
			SecretAccessKey: root.secret,
			PathStyle:       true,
		}
		return bindingproxy.Services{Buckets: s3store.New(s3store.Config{
			Objects:      store.Client(),
			Internal:     store.Presigner(),
			External:     func(context.Context) (s3store.PresignAPI, string) { return store.Presigner(), store.Endpoint },
			Callbacks:    s3store.HTTPPoster{},
			PostPolicies: true,
			Sessions:     s3store.SessionsBucket() + "/" + storeRef(ref).Name.String() + "/" + buildSessions,
			Granted:      buckets,
		})}, stopForward, nil
	})
}
