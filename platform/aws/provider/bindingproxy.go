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
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		deployed, err := p.bootstrapped(ctx, req.Tier)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
			return provider.BindingProxy{}, err
		}
		objects := s3.NewFromConfig(p.aws)
		table := dynamodb.NewFromConfig(p.aws)
		presigner := s3.NewPresignClient(objects)
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			buckets := buildproxy.ListBucketNames(grant)
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: bucket.New(bucket.Config{
				DDB:              table,
				Presigner:        presigner,
				Objects:          objects,
				Table:            deployed.StateTable,
				SessionKeyPrefix: naming.SessionKeyPrefix(req.Slug, req.Env),
				Granted:          func() []string { return slices.Clone(buckets) },
			})}})
		}
		return buildproxy.ServeGrants(served, req.ReportFailure)
	})
}
