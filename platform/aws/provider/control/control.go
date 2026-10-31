package control

import (
	"context"

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
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const Vendor provider.Vendor = "aws"

type Provider struct {
	Options     Options
	Config      aws.Config
	Namespace   account.Namespace
	WrittenBy   provider.WrittenBy
	WebIdentity *provider.WebIdentity

	deployed  memo[environment.Tier, account.Reading]
	params    memo[tierEdge, account.TierParams]
	layers    memo[environment.Tier, map[string]string]
	partReads tier.Reads
	identity  memo[struct{}, *sts.GetCallerIdentityOutput]
	opened    edges.Opened
	sts       STSAPI

	liveness.Net
}

func New(ctx context.Context, settings provider.Settings) (*Provider, error) {
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
	if err := account.Namespace(ns).RefuseReservedPrefix(); err != nil {
		return nil, err
	}
	cfg, err := sdkconfig.Control(ctx, decoded.Region)
	if err != nil {
		return nil, err
	}
	if settings.WebIdentity != nil {
		cfg.Credentials = webIdentityCredentials(cfg, settings.WebIdentity)
	}
	p := NewProvider(decoded, addRegionCheck(cfg, decoded.Region), account.Namespace(ns))
	p.WrittenBy = settings.WrittenBy
	p.WebIdentity = settings.WebIdentity
	return p, nil
}

func NewProvider(options Options, cfg aws.Config, ns account.Namespace) *Provider {
	p := &Provider{Options: options, Config: cfg, Namespace: ns, sts: sts.NewFromConfig(cfg)}
	p.ProbeAddress = emulatedProbeAddress(cfg)
	return p
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor: Vendor,
		Bindings: []provider.BindingType{
			provider.BindingPostgres,
			provider.BindingBucket,
			provider.BindingKV,
			provider.BindingTask,
			provider.BindingTopic,
			provider.BindingRealtime,
		},
		Computes:          []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:             edges.SupportedEdges(),
		DefaultEdge:       edges.DefaultKind,
		Pairings:          edges.Pairings(),
		DNSKinds:          dns.Kinds(),
		RendersTransforms: true,
		StoresArtifacts:   true,
		WorkerCeilings:    queues.WorkerCeilings,

		MaxFunctionBytes: 200 << 20,

		WebIdentity: &provider.WebIdentityFacts{CredentialEnvVars: credentialEnvVars},
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		Preflight:           &provider.PreflightHooks{Plan: refusePlanningPreflight, Apply: refuseApplyingPreflight},
		CheckBucket:         s3store.Check,
		ProgramEdge:         refuseProgrammingEdge,
		EnsureImageRegistry: refuseEnsuringImageRegistry,
		OpenRegistryImages:  refuseOpeningRegistryImages,
		EnvSource:           &provider.EnvSourceHooks{ProveIdentity: p.ProveIdentity, EnsureSync: p.EnsureEnvSourceSync},
		BindingAccess:       &provider.BindingAccessHooks{ForwardPorts: refuseForwardingPorts, ServeProxy: p.ServeBindingProxy},
		Cost:                &provider.CostHooks{Inventory: refuseTakingInventory, Estimate: p.EstimateCost},
		Retirement:          &provider.RetirementHooks{Plan: refusePlanningRetirement, Retire: refuseRetiring},
		ContainerReleases:   &provider.ContainerReleaseHooks{ScaleUp: p.ScaleUpContainerReleases, ScaleDown: p.ScaleDownContainerReleases},

		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	front, err := p.edges().Open(kind, nil)
	if err != nil {
		return nil, err
	}
	b := BootstrapFor(p.Config, front, p.edges(), edges.SupportedEdges(), p.Options.VariablesKey, p.Namespace, p.readBootstrap)
	records := p.PartRecords()
	return bootstrapWithoutEngine{
		cfnParts:         b.Parts,
		describe:         b.Describe,
		describeCFNParts: b.DescribeParts,
		tierParts:        p.TierParts(records, p.describedEngineParts(records)),
	}, nil
}

func (p *Provider) Stacks() provider.Stacks { return stacksWithoutEngine{keyValues: p.KeyValues()} }

func (p *Provider) Artifacts() provider.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.Config), Stores: p}
}

func (p *Provider) KeyValues() records.Store {
	return awsports.KeyValues{Dynamo: dynamodb.NewFromConfig(p.Config), Tables: p}
}

func (p *Provider) Cipher() seal.Cipher {
	return awsports.Cipher{KMS: kms.NewFromConfig(p.Config), Keys: p}
}

func (p *Provider) Credentials() provider.Credentials {
	credentials := CredentialsFor(p.Config, p.Namespace, p.Options.VariablesKey)
	credentials.STS = rememberedIdentity{p}
	return credentials
}

func (p *Provider) Edges() provider.Edges { return p.edges() }

func (p *Provider) Routers() provider.Routers { return p.routers() }

func (p *Provider) DNS() provider.DNS {
	return dns.Registry{Deps: dns.Deps{AWS: p.Config, Cloudflare: p.cloudflareOrigin()}}
}

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return provider.AbsentEngine{} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
