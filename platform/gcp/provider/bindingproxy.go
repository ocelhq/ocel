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
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, bindings []provider.Binding) (bindingproxy.Services, func(), error) {
		messages := make([]*bindingsv1.Binding, 0, len(bindings))
		for _, binding := range bindings {
			message, err := provider.BindingMessage(binding)
			if err != nil {
				return bindingproxy.Services{}, nil, err
			}
			messages = append(messages, message)
		}
		records, err := s3store.NewStaticRecords(messages...)
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		c, err := p.openClients(ctx)
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		store, err := bucket.Open(ctx, c.endpoint)
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		buckets, err := bucket.NewDispatch(ctx, store, records, s3store.HTTPPoster{})
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		return bindingproxy.Services{Buckets: buckets}, func() {}, nil
	})
}
