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
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "aws"

type Options struct {
	Region       string            `json:"region,omitempty" doc:"The AWS region to deploy into."`
	VarsKey      string            `json:"varsKey,omitempty" pattern:"^arn:aws:kms:" doc:"ARN of a KMS key to encrypt this account's variables under. Omit it and ocel bootstrap --features vars-key makes a key ocel owns."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the ARN of an already-issued ACM certificate. A hostname left off gets a certificate ocel requests, validates and deletes again."`
}

type Provider struct {
	options    Options
	transforms []string
	aws        aws.Config
	namespace  bootstrap.Namespace

	deployed memo[providerkit.Class, bootstrap.Deployed]
	params   memo[classEdge, bootstrap.ClassParams]
	account  memo[struct{}, string]

	releases *deploy.Stacks

	providerkit.NetLiveness
}

func New(ctx context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	ns, err := providerkit.NamespaceFromEnv()
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
	p.Front = emulatedFront(cfg)
	p.releases = deploy.NewStacks(deploy.ResolverFunc(p.release), &deploy.Realized{})
	return p
}

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:            Vendor,
		Bindings:          deploy.Serves(),
		Computes:          []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer},
		RendersTransforms: true,
	}
}

func (p *Provider) Hooks() providerkit.Hooks {
	return providerkit.Hooks{
		PreflightDeploy:     p.PreflightDeploy,
		VerifyGrants:        p.VerifyGrants,
		InspectStack:        p.InspectStack,
		PackApp:             p.PackApp,
		EmbedCode:           p.EmbedCode,
		WarmFunctions:       p.WarmFunctions,
		ProgramEdge:         p.ProgramEdge,
		EnsureImageRegistry: p.EnsureImageRegistry,
		RegistryImages:      p.RegistryImages,
		ShapeCost:           p.ShapeCost,
		EstimateCost:        p.EstimateCost,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrap, error) {
	front, err := p.edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return settling{Bootstrap: control.BootstrapFor(p.aws, front, p.edges(), p.options.VarsKey, p.namespace), settled: p.forget}, nil
}

func (p *Provider) Stacks() providerkit.Stacks { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.aws), Stores: p}
}

func (p *Provider) Records() providerkit.RecordStore {
	return awsports.Records{Dynamo: dynamodb.NewFromConfig(p.aws), Tables: p}
}

func (p *Provider) Cipher() providerkit.Cipher {
	return awsports.Cipher{KMS: kms.NewFromConfig(p.aws), Keys: p}
}

func (p *Provider) Credentials() providerkit.Credentials {
	return control.CredentialsFor(p.aws, p.namespace)
}

func (p *Provider) Edges() providerkit.Edges { return p.edges() }

func (p *Provider) DNS() providerkit.DNS {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
}

func (p *Provider) Certificates() providerkit.Certificates { return certificates{p} }

func (p *Provider) Connector() providerkit.Connector { return connector{p} }

func (p *Provider) Runtime() providerkit.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() providerkit.Liveness { return &p.NetLiveness }
