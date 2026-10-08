package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	var buckets []*bindingsv1.Binding
	var unserved []string
	for _, binding := range req.Bindings {
		if binding.Type != provider.BindingBucket {
			unserved = append(unserved, binding.Name)
			continue
		}
		message, err := provider.BindingMessage(binding)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		buckets = append(buckets, message)
	}
	var services bindingproxy.Services
	if len(buckets) > 0 {
		c, err := p.openClients(ctx)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		store, err := bucket.Open(ctx, c.endpoint)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		records, err := s3store.NewStaticRecords(buckets...)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		if services.Buckets, err = bucket.NewDispatch(ctx, store, records, s3store.HTTPPoster{}); err != nil {
			return provider.BindingProxy{}, err
		}
	}
	served, err := bindingproxy.Serve(services)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	served.Watch(req.ReportFailure)
	return provider.BindingProxy{Address: served.Address, SessionToken: served.Token, Unserved: unserved, Close: func() { _ = served.Close() }}, nil
}
