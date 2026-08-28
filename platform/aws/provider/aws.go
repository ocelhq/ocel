package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "aws"

const artifactRootDirName = ".ocel/output"

type Options struct {
	Region       string            `json:"region,omitempty"`
	Transforms   []string          `json:"transforms,omitempty"`
	Certificates map[string]string `json:"certificates,omitempty"`
}

type Provider struct {
	options Options
	aws     aws.Config

	deployed memo[providerkit.Class, bootstrap.Deployed]
	params   memo[classEdge, bootstrap.ClassParams]
	account  memo[struct{}, string]

	releases *deploy.Releaser
}

type classEdge struct {
	class providerkit.Class
	kind  edge.Kind
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
	return NewProvider(decoded, cfg), nil
}

func NewProvider(options Options, cfg aws.Config) *Provider {
	p := &Provider{options: options, aws: cfg}
	p.releases = deploy.NewReleaser(deploy.ResolverFunc(p.release), &deploy.Realized{})
	return p
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType { return deploy.Serves() }

func (p *Provider) Region() string { return p.aws.Region }

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrapper, error) {
	front, err := p.edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return settling{Bootstrapper: control.BootstrapperFor(p.aws, front, p.edges()), settled: p.forget}, nil
}

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
}

func (p *Provider) Releases() providerkit.Releaser { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore {
	return awsports.Artifacts{S3: s3.NewFromConfig(p.aws), Stores: p}
}

func (p *Provider) Records() providerkit.RecordStore {
	return awsports.Records{Dynamo: dynamodb.NewFromConfig(p.aws), Tables: p}
}

func (p *Provider) Sealer() providerkit.Sealer {
	return awsports.Sealer{KMS: kms.NewFromConfig(p.aws), Keys: p}
}

func (p *Provider) Credentials() providerkit.Credentials { return control.CredentialsFor(p.aws) }

func (p *Provider) Edges() providerkit.EdgeRegistry { return p.edges() }

func (p *Provider) DNS() providerkit.DNSRegistry {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
}

func (p *Provider) Membrane(context.Context) ([]byte, error) {
	return payloads.MembraneLayer().Bytes, nil
}

func (p *Provider) Warm(ctx context.Context, targets []string, report providerkit.Reporter) error {
	return p.releases.Warm(ctx, targets, report)
}

func (p *Provider) EmbedCode(ctx context.Context, function string, artifact providerkit.ArtifactRef, report providerkit.Reporter) error {
	return p.releases.EmbedCode(ctx, function, artifact, report)
}

func (p *Provider) PackApp(ctx context.Context, packing providerkit.AppPacking, report providerkit.Reporter) (providerkit.AppPack, error) {
	return p.releases.PackApp(ctx, packing, report)
}

func (p *Provider) Inspect(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.releases.Inspect(ctx, ref)
}

func (p *Provider) VerifyGrants(_ context.Context, link providerkit.Link) error {
	return deploy.VerifyGrants(link)
}

func (p *Provider) PreflightDeploy(ctx context.Context, pre providerkit.DeployPreflight) error {
	return p.releases.Preflight(ctx, pre)
}

func (p *Provider) EdgeProgram(ctx context.Context, req providerkit.EdgeProgramRequest) (providerkit.EdgeProgram, error) {
	held, err := p.bootstrapped(ctx, req.Class)
	if err != nil {
		return providerkit.EdgeProgram{}, err
	}
	params, err := p.classParams(ctx, req.Class, req.Kind)
	if err != nil {
		return providerkit.EdgeProgram{}, err
	}
	program := deploy.EdgeProgram{
		Class:             req.Class,
		Kind:              req.Kind,
		Slug:              req.Slug,
		Env:               req.Env,
		PreviewBaseDomain: req.PreviewBaseDomain,
		Apps:              req.Apps,
		Worker: deploy.WorkerFacts{
			Region:             p.aws.Region,
			StateTable:         held.StateTable,
			AssetBucket:        held.AssetBucket,
			ImageOptimizerURL:  held.ImageOptimizerURL,
			RevalidateQueueURL: held.RevalidateQueueURL,
		},
		StoreScriptName:     params.DeploymentsStore.ScriptName,
		StoreEndpoint:       params.DeploymentsStore.Endpoint,
		StoreBootstrapCred:  params.DeploymentsStore.BootstrapCred,
		ISRWriterScriptName: params.ISRWriter.ScriptName,
	}
	if params.EdgeCredentialsErr == nil {
		program.Worker.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		program.Worker.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	}
	if params.EdgeValuesErr == nil {
		program.Values = params.EdgeValues
	}
	return program.Build()
}

func (p *Provider) edges() edges.Registry {
	return edges.Registry{Deps: edges.Deps{
		AWS:          func(context.Context) (aws.Config, error) { return p.aws, nil },
		Certificates: p.options.Certificates,
	}}
}

func (p *Provider) Table(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.StateTable, nil
}

func (p *Provider) Key(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsKeyARN, nil
}

func (p *Provider) Buckets(ctx context.Context, class providerkit.Class) (awsports.Buckets, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return awsports.Buckets{}, err
	}
	buckets := awsports.Buckets{Functions: held.ArtifactBucket, Assets: held.AssetBucket}
	for _, kind := range bootstrap.EdgeKindsFor(held.Features.Names()) {
		params, err := p.classParams(ctx, class, kind)
		if err != nil {
			return buckets, err
		}
		if params.CacheStore.Bucket != "" {
			buckets.Caches = append(buckets.Caches, awsports.CacheBucket{
				Name: params.CacheStore.Bucket,
				S3:   cacheStoreClient(params.CacheStore),
			})
		}
	}
	return buckets, nil
}

func cacheStoreClient(store bootstrap.CacheStore) awsports.S3API {
	if store.Endpoint == "" {
		return nil
	}
	return s3.New(s3.Options{
		Region:       store.Region,
		BaseEndpoint: aws.String(store.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(store.AccessKeyID, store.SecretAccessKey, ""),
	})
}

func (p *Provider) bootstrapped(ctx context.Context, class providerkit.Class) (bootstrap.Deployed, error) {
	return p.deployed.resolve(class, func() (bootstrap.Deployed, error) {
		return bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(p.aws), string(class))
	})
}

func (p *Provider) classParams(ctx context.Context, class providerkit.Class, kind edge.Kind) (bootstrap.ClassParams, error) {
	return p.params.resolve(classEdge{class: class, kind: kind}, func() (bootstrap.ClassParams, error) {
		if kind == "" {
			return bootstrap.ReadCoreParams(ctx, ssm.NewFromConfig(p.aws), string(class))
		}
		return bootstrap.ReadClassParams(ctx, ssm.NewFromConfig(p.aws), string(class), kind)
	})
}

func (p *Provider) accountID(ctx context.Context) (string, error) {
	return p.account.resolve(struct{}{}, func() (string, error) {
		out, err := sts.NewFromConfig(p.aws).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return "", fmt.Errorf("resolve AWS account id: %w", err)
		}
		return aws.ToString(out.Account), nil
	})
}

func (p *Provider) release(ctx context.Context, scope deploy.Scope) (deploy.Config, error) {
	held, err := p.bootstrapped(ctx, scope.Class)
	if err != nil {
		return deploy.Config{}, err
	}
	if err := p.standing(held, scope.Class); err != nil {
		return deploy.Config{}, err
	}
	params, err := p.classParams(ctx, scope.Class, scope.Edge)
	if err != nil {
		return deploy.Config{}, err
	}
	account, err := p.accountID(ctx)
	if err != nil {
		return deploy.Config{}, err
	}
	store := values.Store{Records: p.Records(), Sealer: p.Sealer()}
	referenced, err := store.ReferenceOwners(ctx, values.Scope{Project: scope.Slug, Class: scope.Class})
	if err != nil {
		return deploy.Config{}, err
	}

	root := projectRoot()
	cfg := deploy.Config{
		Region:        p.aws.Region,
		BackendURL:    naming.StateBackendURL(held.StateBucket, scope.Slug),
		Passphrase:    params.Passphrase,
		PulumiProject: naming.PulumiProject(scope.Slug),
		Secrets:       secretsmanager.NewFromConfig(p.aws),

		Tags: &tagclock.Sweeper{Dynamo: dynamodb.NewFromConfig(p.aws), Table: held.StateTable},

		Class:          scope.Class,
		Slug:           scope.Slug,
		Env:            scope.Env,
		StateTable:     held.StateTable,
		StateTableARN:  stateTableARN(p.aws.Region, account, held.StateTable),
		VarsKeyARN:     held.VarsKeyARN,
		AppBoundaryARN: held.AppBoundaryARN,
		VarsReferenced: referenced,

		ArtifactRoot:       filepath.Join(root, artifactRootDirName),
		ArtifactBucket:     held.ArtifactBucket,
		AssetBucket:        held.AssetBucket,
		ImageOptimizerURL:  held.ImageOptimizerURL,
		RevalidateQueueURL: held.RevalidateQueueURL,

		CacheStoreBucket:   params.CacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(params.CacheStore),

		Uploader:    s3.NewFromConfig(p.aws),
		Getter:      s3.NewFromConfig(p.aws),
		Invoker:     lambda.NewFromConfig(p.aws),
		CodeUpdater: lambda.NewFromConfig(p.aws),

		StoreScriptName:    params.DeploymentsStore.ScriptName,
		StoreEndpoint:      params.DeploymentsStore.Endpoint,
		StoreBootstrapCred: params.DeploymentsStore.BootstrapCred,

		ISRWriterEndpoint:      params.ISRWriter.Endpoint,
		ISRWriterBootstrapCred: params.ISRWriter.BootstrapCred,
		ISRWriterScriptName:    params.ISRWriter.ScriptName,
		ISRWriterSeed:          params.ISRWriterSeed,

		OriginSecret: params.OriginSecret,

		Transform: p.transforms(root),
	}
	if params.EdgeCredentialsErr == nil {
		cfg.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		cfg.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	}
	if params.EdgeValuesErr == nil {
		cfg.EdgeValues = params.EdgeValues
	}
	return cfg, nil
}

func (p *Provider) standing(held bootstrap.Deployed, class providerkit.Class) error {
	command := providerkit.BootstrapCommand(class)
	for _, missing := range []struct {
		held string
		what string
	}{
		{held.StateBucket, "state bucket"},
		{held.ArtifactBucket, "artifact bucket"},
		{held.AssetBucket, "asset bucket"},
		{held.StateTable, "state table"},
		{held.VarsTable, "variable store"},
		{held.VarsKeyARN, "variable store"},
	} {
		if missing.held == "" {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"account bootstrap is present but its %s is missing (a partial rollback?); re-run `%s`", missing.what, command)
		}
	}
	return nil
}

func (p *Provider) transforms(root string) transform.Evaluator {
	if len(p.options.Transforms) == 0 {
		return nil
	}
	return transform.NodePass{Root: root, Modules: p.options.Transforms}
}

func stateTableARN(region, account, table string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", region, account, table)
}

func cacheStoreUploader(store bootstrap.CacheStore) deploy.ArtifactUploader {
	if store.Bucket == "" {
		return nil
	}
	return s3.NewFromConfig(aws.Config{
		Region:      store.Region,
		Credentials: credentials.NewStaticCredentialsProvider(store.AccessKeyID, store.SecretAccessKey, ""),
		Retryer:     sdkconfig.ControlRetryer,
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(store.Endpoint)
	})
}

func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

type memo[K comparable, V any] struct {
	mu   sync.Mutex
	held map[K]V
}

func (m *memo[K, V]) resolve(key K, fill func() (V, error)) (V, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, filled := m.held[key]; filled {
		return held, nil
	}
	value, err := fill()
	if err != nil {
		var zero V
		return zero, err
	}
	if m.held == nil {
		m.held = map[K]V{}
	}
	m.held[key] = value
	return value, nil
}

func (m *memo[K, V]) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = nil
}

type settling struct {
	providerkit.Bootstrapper
	settled func()
}

func (s settling) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if err := s.Bootstrapper.Apply(ctx, req, report); err != nil {
		return err
	}
	s.settled()
	return nil
}

func (s settling) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	if err := s.Bootstrapper.Remove(ctx, class, report); err != nil {
		return err
	}
	s.settled()
	return nil
}

var (
	_ providerkit.Provider       = (*Provider)(nil)
	_ providerkit.Warmer         = (*Provider)(nil)
	_ providerkit.CodeEmbedder   = (*Provider)(nil)
	_ providerkit.MembraneSource = (*Provider)(nil)
	_ providerkit.StackInspector = (*Provider)(nil)
	_ providerkit.Certifier      = (*Provider)(nil)
	_ providerkit.Bootstrapper   = settling{}
	_ awsports.Tables            = (*Provider)(nil)
	_ awsports.Keys              = (*Provider)(nil)
	_ awsports.Stores            = (*Provider)(nil)
)
