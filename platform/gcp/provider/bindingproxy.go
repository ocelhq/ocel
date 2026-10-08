package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		c, err := p.openClients(ctx)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		store, err := bucket.Open(ctx, c.endpoint)
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
		return buildproxy.ServeGrants(served, req.ReportFailure)
	})
}
