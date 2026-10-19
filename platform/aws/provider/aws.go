package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
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
	writtenBy  provider.WrittenBy
	aws        aws.Config
	namespace  account.Namespace

	deployed  memo[environment.Tier, account.Reading]
	params    memo[tierEdge, account.TierParams]
	layers    memo[environment.Tier, map[string]string]
	partReads tier.Reads
	identity  memo[struct{}, *sts.GetCallerIdentityOutput]
	opened    edges.Opened
	sts       control.STSAPI

	stacks *deploy.Stacks

	liveness.Net
}

func New(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	if decoded.Bytecode.Embed && !decoded.Bytecode.Cache {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"option %q embeds the compile cache and %q warms none: set %q too, or drop embed", "bytecode.embed", "bytecode.cache", "bytecode.cache")
	}
	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	cfg, err := sdkconfig.Control(ctx, decoded.Region)
	if err != nil {
		return nil, err
	}
	p := NewProvider(decoded, settings.Transforms, addRegionCheck(cfg, decoded.Region), account.Namespace(ns))
	p.projectDir = settings.ProjectDir
	p.writtenBy = settings.WrittenBy
	return p, nil
}

func NewProvider(options Options, transforms []string, cfg aws.Config, ns account.Namespace) *Provider {
	p := &Provider{options: options, transforms: transforms, aws: cfg, namespace: ns, sts: sts.NewFromConfig(cfg)}
	p.ProbeAddress = emulatedProbeAddress(cfg)
	p.stacks = deploy.NewStacks(p.release, &deploy.Realized{})
	p.stacks.Statements = rdsdata.NewFromConfig(cfg)
	return p
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:            Vendor,
		Bindings:          awsresources.Types(),
		Computes:          []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:             edges.SupportedEdges(),
		DefaultEdge:       edges.DefaultKind,
		Pairings:          edges.Pairings(),
		DNSKinds:          dns.Kinds(),
		RendersTransforms: true,
		StoresArtifacts:   true,
		WorkerCeilings:    topic.WorkerCeilings,

		MaxFunctionBytes: function.MaxBytes,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		Preflight:           &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight},
		CheckBucket:         s3store.Check,
		ProgramEdge:         p.ProgramEdge,
		EnsureImageRegistry: p.EnsureImageRegistry,
		OpenRegistryImages:  p.OpenRegistryImages,
		EnvSource:           &provider.EnvSourceHooks{ProveIdentity: p.ProveIdentity, EnsureSync: p.EnsureEnvSourceSync},
		BindingAccess:       &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: p.ServeBindingProxy},
		Cost:                &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
		Retirement:          &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire},
		ContainerReleases:   &provider.ContainerReleaseHooks{ScaleUp: p.ScaleUpContainerReleases, ScaleDown: p.ScaleDownContainerReleases},

		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	front, err := p.edges().Open(kind, nil)
	if err != nil {
		return nil, err
	}
	b := control.BootstrapFor(p.aws, front, p.edges(), edges.SupportedEdges(), p.options.VariablesKey, p.namespace, p.readBootstrap)
	records := p.partRecords()
	b.TierParts = p.tierParts(records)
	return tierBootstrap{Bootstrap: b, forget: p.forget, key: p.Key, keyValues: records.KeyValues, parts: b.TierParts}, nil
}

func (p *Provider) Stacks() provider.Stacks { return p.stacks }

func (p *Provider) Artifacts() provider.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.aws), Stores: p}
}

func (p *Provider) KeyValues() records.Store {
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
