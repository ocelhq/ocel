package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func (p *Provider) edges() edges.Registry {
	return edges.Registry{Deps: edges.Deps{
		AWS:          func(context.Context) (aws.Config, error) { return p.aws, nil },
		Certificates: p.options.Certificates,
		Namespace:    p.namespace,
	}}
}
