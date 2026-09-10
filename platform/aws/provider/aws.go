package provider

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "aws"

const artifactRootDirName = constants.ProjectStateDirName + "/output"

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

	releases *deploy.Releaser
}

type classEdge struct {
	class providerkit.Class
	kind  edge.Kind
}

func New(ctx context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](settings.Options)
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
	p.releases = deploy.NewReleaser(deploy.ResolverFunc(p.release), &deploy.Realized{})
	return p
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) RendersTransforms() {}

func (p *Provider) Serves() []providerkit.BindingType { return deploy.Serves() }

func (p *Provider) Computes() []providerkit.Compute {
	return []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
}

func (p *Provider) ImageRegistry(ctx context.Context, _ providerkit.Class, _ []string) (providerkit.RegistryTarget, error) {
	return registry.Resolve(ctx, ecr.NewFromConfig(p.aws))
}

func (p *Provider) Images(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	if !registry.Owns(target) {
		return providerkit.RegistryImages(target), nil
	}
	return registry.Images(target, ecr.NewFromConfig(p.aws)), nil
}

func (p *Provider) Region() string { return p.aws.Region }

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrapper, error) {
	front, err := p.edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return settling{Bootstrapper: control.BootstrapperFor(p.aws, front, p.edges(), p.options.VarsKey, p.namespace), settled: p.forget}, nil
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

func (p *Provider) Credentials() providerkit.Credentials {
	return control.CredentialsFor(p.aws, p.namespace)
}

func (p *Provider) Edges() providerkit.EdgeRegistry { return p.edges() }

func (p *Provider) DNS() providerkit.DNSRegistry {
	return dns.Registry{Deps: dns.Deps{AWS: p.aws}}
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

func (p *Provider) VerifyGrants(_ context.Context, binding providerkit.Binding) error {
	return deploy.VerifyGrants(binding)
}

func (p *Provider) PreflightDeploy(ctx context.Context, pre providerkit.DeployPreflight) error {
	if err := refuseContainersBehindFunctionEdge(pre); err != nil {
		return err
	}
	if err := p.publishRuntimeLayers(ctx, pre); err != nil {
		return err
	}
	return p.releases.Preflight(ctx, pre)
}

func (p *Provider) publishRuntimeLayers(ctx context.Context, pre providerkit.DeployPreflight) error {
	if pre.Dry {
		return nil
	}
	class := pre.Plan.Class
	held, err := p.bootstrapped(ctx, class)
	if err != nil || !held.Present {
		return err
	}
	published, err := bootstrap.EnsureRuntimeLayers(ctx, bootstrap.APIs{
		CFN:   cloudformation.NewFromConfig(p.aws),
		Store: s3.NewFromConfig(p.aws),
	}, p.namespace, string(class), bootstrap.RuntimeLayerRequest{
		ArtifactBucket: held.ArtifactBucket,
		Writer:         pre.Writer,
	}, saying(pre.Report))
	if err != nil {
		return err
	}
	if !maps.Equal(published, held.RuntimeLayers) {
		p.deployed.forget()
	}
	return nil
}

func saying(report providerkit.Reporter) func(string) {
	if report == nil {
		return nil
	}
	return report.Say
}

func refuseContainersBehindFunctionEdge(pre providerkit.DeployPreflight) error {
	if pre.Edge == edges.DefaultKind {
		return nil
	}
	for _, app := range pre.Plan.Apps {
		if app.Compute() != providerkit.ComputeContainer {
			continue
		}
		return providerkit.Refuse(providerkit.CodeInvalid,
			"app %s runs as a container, and the %q edge reaches a release's entry function rather than an origin that demands the class's secret, so it has no way to reach one: front this project with %q, or give %s `compute: \"serverless\"`",
			app.App, pre.Edge, edges.DefaultKind, app.App)
	}
	return nil
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
		Namespace:    p.namespace,
	}}
}

func (p *Provider) Table(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsTable, nil
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
		return bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(p.aws), p.namespace, string(class))
	})
}

func (p *Provider) classParams(ctx context.Context, class providerkit.Class, kind edge.Kind) (bootstrap.ClassParams, error) {
	return p.params.resolve(classEdge{class: class, kind: kind}, func() (bootstrap.ClassParams, error) {
		if kind == "" {
			return bootstrap.ReadCoreParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(class))
		}
		return bootstrap.ReadClassParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(class), kind)
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
		BackendURL:    stateBackendURL(held.StateBucket, scope.Slug),
		Passphrase:    params.Passphrase,
		PulumiProject: naming.PulumiProject(scope.Slug),
		Secrets:       secretsmanager.NewFromConfig(p.aws),

		Tags:    &tagclock.Sweeper{Dynamo: dynamodb.NewFromConfig(p.aws), Table: held.StateTable},
		Records: p.Records(),
		Rules:   elasticloadbalancingv2.NewFromConfig(p.aws),

		Class:          scope.Class,
		Slug:           scope.Slug,
		Env:            scope.Env,
		StateTable:     held.StateTable,
		StateTableARN:  tableARN(p.aws.Region, account, held.StateTable),
		VarsTable:      held.VarsTable,
		VarsTableARN:   tableARN(p.aws.Region, account, held.VarsTable),
		VarsKeyARN:     held.VarsKeyARN,
		AppBoundaryARN: held.AppBoundaryARN,
		VarsReferenced: referenced,

		RuntimeLayers: held.RuntimeLayers,

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

		Transform: p.transformPass(root),
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
	} {
		if missing.held == "" {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"account bootstrap is present but its %s is missing (a partial rollback?); re-run `%s`", missing.what, command)
		}
	}
	return nil
}

func (p *Provider) transformPass(root string) transform.Evaluator {
	if len(p.transforms) == 0 {
		return nil
	}
	return transform.NodePass{Root: root, Modules: p.transforms}
}

func tableARN(region, account, table string) string {
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
	_ providerkit.StackInspector = (*Provider)(nil)
	_ providerkit.Certifier      = (*Provider)(nil)
	_ providerkit.ImageRegistry  = (*Provider)(nil)
	_ providerkit.ImagePusher    = (*Provider)(nil)
	_ providerkit.Bootstrapper   = settling{}
	_ awsports.Tables            = (*Provider)(nil)
	_ awsports.Keys              = (*Provider)(nil)
	_ awsports.Stores            = (*Provider)(nil)
)

const s3Scheme = "s3"

func stateBackendURL(bucket, slug string) string {
	backend := naming.StateBackendURL(s3Scheme, bucket, slug)
	endpoint := os.Getenv("AWS_ENDPOINT_URL_S3")
	if endpoint == "" {
		endpoint = os.Getenv("AWS_ENDPOINT_URL")
	}
	if endpoint == "" {
		return backend
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return backend
	}
	query := url.Values{"endpoint": {parsed.Host}, "s3ForcePathStyle": {"true"}}
	if parsed.Scheme == "http" {
		query.Set("disableSSL", "true")
	}
	return backend + "?" + query.Encode()
}
