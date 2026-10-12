package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const Vendor provider.Vendor = "aws"

type Provider struct {
	options    Options
	transforms []string
	projectDir string
	aws        aws.Config
	namespace  bootstrap.Namespace

	deployed memo[environment.Tier, bootstrap.Reading]
	params   memo[tierEdge, bootstrap.TierParams]
	identity memo[struct{}, *sts.GetCallerIdentityOutput]
	opened   edges.Opened
	sts      control.STSAPI

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
	p := NewProvider(decoded, settings.Transforms, cfg, bootstrap.Namespace(ns))
	p.projectDir = settings.ProjectDir
	return p, nil
}

func NewProvider(options Options, transforms []string, cfg aws.Config, ns bootstrap.Namespace) *Provider {
	p := &Provider{options: options, transforms: transforms, aws: cfg, namespace: ns, sts: sts.NewFromConfig(cfg)}
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
		Pairings:          edges.Pairings(),
		DNSKinds:          dns.Kinds(),
		RendersTransforms: true,
		StoresArtifacts:   true,
		WorkerCeilings:    deploy.WorkerCeilings,

		MaxFunctionBytes: deploy.MaxFunctionBytes,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		PreflightDeploy:     p.PreflightDeploy,
		VerifyGrants:        p.VerifyGrants,
		CheckBucket:         s3store.Check,
		InspectStack:        p.InspectStack,
		PackApp:             p.PackApp,
		EmbedCode:           p.EmbedCode,
		WarmFunctions:       p.WarmFunctions,
		ProgramEdge:         p.ProgramEdge,
		EnsureImageRegistry: p.EnsureImageRegistry,
		OpenRegistryImages:  p.OpenRegistryImages,
		ProveIdentity:       p.ProveIdentity,
		ForwardPorts:        p.ForwardPorts,
		ServeBindingProxy:   p.ServeBindingProxy,
		Cost:                &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	front, err := p.edges().Open(kind, nil)
	if err != nil {
		return nil, err
	}
	return forgetting{Bootstrap: control.BootstrapFor(p.aws, front, p.edges(), edges.SupportedEdges(), p.options.VariablesKey, p.namespace, p.readBootstrap), forget: p.forget, key: p.Key, keyValues: p.KeyValues()}, nil
}

func (p *Provider) Stacks() provider.Stacks { return p.stacks }

func (p *Provider) Artifacts() provider.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.aws), Stores: p}
}

func (p *Provider) KeyValues() keyvalue.Store {
	return awsports.KeyValues{Dynamo: dynamodb.NewFromConfig(p.aws), Tables: p}
}

func (p *Provider) Cipher() seal.Cipher {
	return awsports.Cipher{KMS: kms.NewFromConfig(p.aws), Keys: p}
}

func (p *Provider) Credentials() provider.Credentials {
	credentials := control.CredentialsFor(p.aws, p.namespace, p.options.VariablesKey)
	credentials.STS = rememberedIdentity{p}
	return credentials
}

func (p *Provider) Edges() provider.Edges { return p.edges() }

func (p *Provider) Routers() provider.Routers { return p.routers() }

func (p *Provider) DNS() provider.DNS {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
}

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
