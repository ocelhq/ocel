package aws

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/aws/provider/bucket"
)

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
		return provider.BindingProxy{}, err
	}
	var buckets []string
	var unserved []string
	for _, binding := range req.Bindings {
		if binding.Type == provider.BindingBucket {
			buckets = append(buckets, binding.Properties[provider.PropertyBucket])
			continue
		}
		unserved = append(unserved, binding.Name)
	}
	var services bindingproxy.Services
	if len(buckets) > 0 {
		objects := s3.NewFromConfig(p.aws)
		services.Buckets = bucket.New(bucket.Config{
			DDB:              dynamodb.NewFromConfig(p.aws),
			Presigner:        s3.NewPresignClient(objects),
			Objects:          objects,
			Table:            deployed.StateTable,
			SessionKeyPrefix: naming.SessionKeyPrefix(req.Slug, req.Env),
			Granted:          func() []string { return slices.Clone(buckets) },
		})
	}
	served, err := bindingproxy.Serve(services)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	served.Watch(req.ReportFailure)
	return provider.BindingProxy{Address: served.Address, SessionToken: served.Token, Unserved: unserved, Close: func() { _ = served.Close() }}, nil
}
