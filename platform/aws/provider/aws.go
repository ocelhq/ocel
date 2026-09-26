package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/liveness"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor provider.Vendor = "aws"

type Provider struct {
	options    Options
	transforms []string
	aws        aws.Config
	namespace  bootstrap.Namespace

	deployed memo[edge.Class, bootstrap.Deployed]
	params   memo[classEdge, bootstrap.ClassParams]
	account  memo[struct{}, string]

	stacks *deploy.Stacks

	liveness.Net
}

func New(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	cfg, err := sdkconfig.Control(ctx, decoded.Region)
	if err != nil {
		return nil, err
	}
	return NewProvider(decoded, settings.Transforms, cfg, bootstrap.Namespace(ns)), nil
}

func NewProvider(options Options, transforms []string, cfg aws.Config, ns bootstrap.Namespace) *Provider {
	p := &Provider{options: options, transforms: transforms, aws: cfg, namespace: ns}
	p.ProbeAddress = emulatedProbeAddress(cfg)
	p.stacks = deploy.NewStacks(p.release, &deploy.Realized{})
	return p
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:            Vendor,
		Bindings:          deploy.Serves(),
		Computes:          []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:             edges.SupportedEdges(),
		DefaultEdge:       edges.DefaultKind,
		DNSKinds:          dns.Kinds(),
		RendersTransforms: true,
		StoresArtifacts:   true,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		PreflightDeploy:     p.PreflightDeploy,
		VerifyGrants:        p.VerifyGrants,
		InspectStack:        p.InspectStack,
		PackApp:             p.PackApp,
		EmbedCode:           p.EmbedCode,
		WarmFunctions:       p.WarmFunctions,
		ProgramEdge:         p.ProgramEdge,
		EnsureImageRegistry: p.EnsureImageRegistry,
		OpenRegistryImages:  p.OpenRegistryImages,
		Cost:                &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	front, err := p.edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return forgetting{Bootstrap: control.BootstrapFor(p.aws, front, p.edges(), edges.SupportedEdges(), p.options.VarsKey, p.namespace), forget: p.forget}, nil
}

func (p *Provider) Stacks() provider.Stacks { return p.stacks }

func (p *Provider) Artifacts() provider.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.aws), Stores: p}
}

func (p *Provider) Records() records.Store {
	return awsports.Records{Dynamo: dynamodb.NewFromConfig(p.aws), Tables: p}
}

func (p *Provider) Cipher() records.Cipher {
	return awsports.Cipher{KMS: kms.NewFromConfig(p.aws), Keys: p}
}

func (p *Provider) Credentials() provider.Credentials {
	return control.CredentialsFor(p.aws, p.namespace)
}

func (p *Provider) Edges() provider.Edges { return p.edges() }

func (p *Provider) DNS() provider.DNS {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
}

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() images.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }
