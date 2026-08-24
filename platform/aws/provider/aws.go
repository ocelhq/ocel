package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "aws"

type Options struct {
	Region  string `json:"region,omitempty"`
	Profile string `json:"profile,omitempty"`
}

type Provider struct {
	options  Options
	aws      aws.Config
	deployed bootstrap.Deployed
	writer   providerkit.Writer

	records   awsports.Records
	artifacts awsports.Artifacts
	sealer    awsports.Sealer
	stacks    awsports.Stacks
	releases  *deploy.Releaser
	front     edge.Edge
}

type Deps struct {
	AWS      aws.Config
	Deployed bootstrap.Deployed
	Writer   providerkit.Writer
	Edge     edge.Edge
	Config   deploy.Config
	Realized *deploy.Realized
}

func New(ctx context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	cfg, err := sdkconfig.Control(ctx, decoded.Region)
	if err != nil {
		return nil, err
	}
	return NewProvider(decoded, Deps{AWS: cfg}), nil
}

func NewProvider(options Options, deps Deps) *Provider {
	ddb := dynamodb.NewFromConfig(deps.AWS)
	records := awsports.Records{Dynamo: ddb, Table: deps.Deployed.StateTable}
	stacks := awsports.Stacks{
		Records: records,
		Tags:    &tagclock.Sweeper{Dynamo: ddb, Table: deps.Deployed.StateTable},
	}
	cfg := deps.Config
	cfg.Stacks = stacks
	realized := deps.Realized
	if realized == nil {
		realized = &deploy.Realized{}
	}
	return &Provider{
		options:  options,
		aws:      deps.AWS,
		deployed: deps.Deployed,
		writer:   deps.Writer,
		records:  records,
		sealer:   awsports.Sealer{KMS: kms.NewFromConfig(deps.AWS), KeyARN: deps.Deployed.VarsKeyARN},
		stacks:   stacks,
		front:    deps.Edge,
		artifacts: awsports.Artifacts{
			S3:        s3.NewFromConfig(deps.AWS),
			Functions: deps.Deployed.ArtifactBucket,
			Assets:    deps.Deployed.AssetBucket,
			Cache:     cfg.CacheStoreBucket,
		},
		releases: deploy.NewReleaser(cfg, realized),
	}
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType { return deploy.Serves() }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) Bootstrap() providerkit.Bootstrapper {
	return control.BootstrapperFor(p.aws, p.front, p.writer)
}

func (p *Provider) Releases() providerkit.Releaser { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return control.CredentialsFor(p.aws) }

func (p *Provider) Edges() providerkit.EdgeRegistry {
	return edges.Registry{Deps: edges.Deps{AWS: func(context.Context) (aws.Config, error) { return p.aws, nil }}}
}

func (p *Provider) DNS() providerkit.DNSRegistry {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
}

func (p *Provider) Stacks() awsports.Stacks { return p.stacks }

func (p *Provider) Membrane(context.Context) ([]byte, error) {
	return payloads.MembraneLayer().Bytes, nil
}

func (p *Provider) Warm(ctx context.Context, targets []string, report providerkit.Reporter) error {
	return p.releases.Warm(ctx, targets, report)
}

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, report providerkit.Reporter) error {
	return p.releases.EmbedCode(ctx, function, artifact, report)
}

func (p *Provider) Inspect(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.releases.Inspect(ctx, ref)
}

var (
	_ providerkit.Provider       = (*Provider)(nil)
	_ providerkit.Warmer         = (*Provider)(nil)
	_ providerkit.CodeEmbedder   = (*Provider)(nil)
	_ providerkit.MembraneSource = (*Provider)(nil)
	_ providerkit.StackInspector = (*Provider)(nil)
)
