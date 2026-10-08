package aws

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/platform/aws/provider/bucket"
)

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, bindings []provider.Binding) (bindingproxy.Services, func(), error) {
		deployed, err := p.bootstrapped(ctx, req.Tier)
		if err != nil {
			return bindingproxy.Services{}, nil, err
		}
		if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
			return bindingproxy.Services{}, nil, err
		}
		var buckets []string
		for _, binding := range bindings {
			if name := binding.Properties[provider.PropertyBucket]; !slices.Contains(buckets, name) {
				buckets = append(buckets, name)
			}
		}
		objects := s3.NewFromConfig(p.aws)
		return bindingproxy.Services{Buckets: bucket.New(bucket.Config{
			DDB:              dynamodb.NewFromConfig(p.aws),
			Presigner:        s3.NewPresignClient(objects),
			Objects:          objects,
			Table:            deployed.StateTable,
			SessionKeyPrefix: naming.SessionKeyPrefix(req.Slug, req.Env),
			Granted:          func() []string { return slices.Clone(buckets) },
		})}, func() {}, nil
	})
}
