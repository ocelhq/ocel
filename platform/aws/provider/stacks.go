package aws

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

func (p *Provider) release(ctx context.Context, scope deploy.Scope) (deploy.Config, error) {
	deployed, err := p.bootstrapped(ctx, scope.Tier)
	if err != nil {
		return deploy.Config{}, err
	}
	if err := p.requireBootstrapped(deployed, scope.Tier); err != nil {
		return deploy.Config{}, err
	}
	params, err := p.tierParams(ctx, scope.Tier, scope.Edge)
	if err != nil {
		return deploy.Config{}, err
	}
	accountID, err := p.accountID(ctx)
	if err != nil {
		return deploy.Config{}, err
	}
	layers, err := p.runtimeLayers(ctx, scope.Tier)
	if err != nil {
		return deploy.Config{}, err
	}
	optimizer, err := imageoptimizer.ReadURL(ctx, p.partRecords(), scope.Tier)
	if err != nil {
		return deploy.Config{}, err
	}
	clients, err := p.vpcClients(ctx, scope.Tier)
	if err != nil {
		return deploy.Config{}, err
	}
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	referenced, err := store.ReferenceOwners(ctx, variablestore.Scope{Project: scope.Slug, Tier: scope.Tier})
	if err != nil {
		return deploy.Config{}, err
	}

	front, err := p.openEdge(scope.Edge)
	if err != nil {
		return deploy.Config{}, err
	}

	cfg := deploy.Config{
		Namespace:      string(p.namespace),
		Region:         p.aws.Region,
		BackendURL:     stateBackendURL(deployed.Buckets[naming.StorageState], scope.Slug),
		Passphrase:     params.Passphrase,
		PulumiProject:  naming.PulumiProject(scope.Slug),
		Parameters:     ssm.NewFromConfig(p.aws),
		KVTokenRoot:    p.namespace.KVTokenRoot(),
		Clusters:       rds.NewFromConfig(p.aws),
		SigningKeys:    secretsmanager.NewFromConfig(p.aws),
		SigningKeyRoot: p.namespace.SigningKeyRoot(),

		Tags:      &tagclock.Table{Dynamo: dynamodb.NewFromConfig(p.aws), Table: deployed.StateTable},
		KeyValues: p.KeyValues(),
		WrittenBy: p.writtenBy,
		Rules:     elasticloadbalancingv2.NewFromConfig(p.aws),
		Network:   ec2.NewFromConfig(p.aws),

		VPCOrigins: cloudfront.NewFromConfig(p.aws, func(o *cloudfront.Options) { o.Region = certs.CloudFrontRegion }),

		Tier:                scope.Tier,
		Slug:                scope.Slug,
		Edge:                front,
		Env:                 scope.Env,
		StateTable:          deployed.StateTable,
		StateTableARN:       tableARN(p.aws.Region, accountID, deployed.StateTable),
		VariablesTable:      deployed.VariablesTable,
		VariablesTableARN:   tableARN(p.aws.Region, accountID, deployed.VariablesTable),
		VariablesKeyARN:     deployed.VariablesKeyARN,
		AppBoundaryARN:      deployed.AppBoundaryARN,
		AppRolePath:         p.namespace.AppRolePathFor(scope.Tier),
		VPCClients:          clients,
		VariablesReferenced: referenced,

		RuntimeLayers: layers,
		BytecodeCache: p.options.Bytecode.Cache,
		BytecodeEmbed: p.options.Bytecode.Embed,

		ArtifactRoot:      scope.OutputRoot,
		Buckets:           deployed.Buckets,
		ImageOptimizerURL: optimizer,

		Objects:     s3.NewFromConfig(p.aws),
		Getter:      s3.NewFromConfig(p.aws),
		Invoker:     lambda.NewFromConfig(p.aws),
		CodeUpdater: lambda.NewFromConfig(p.aws),

		StoreScriptName:          params.ReleasesStore.ScriptName,
		StoreEndpoint:            params.ReleasesStore.Endpoint,
		StoreBootstrapCredential: params.ReleasesStore.BootstrapCredential,

		PurgeSeed: params.PurgeSeed,

		OriginSecret:         params.OriginSecret.Current,
		PreviousOriginSecret: params.OriginSecret.Previous,

		Transform: p.transformPass(),

		Retention: &resources.ImageRetentionHooks{Reconcile: p.ReconcileImages, Forget: p.ForgetReleases},
	}
	switch {
	case params.EdgeCredentialsErr == nil:
		cfg.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		cfg.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	case !errors.Is(params.EdgeCredentialsErr, account.ErrEdgeCredentialsAbsent):
		cfg.EdgeCredentialsErr = params.EdgeCredentialsErr
	}
	return cfg, nil
}

func (p *Provider) requireBootstrapped(deployed account.Deployed, tier environment.Tier) error {
	command := provider.BootstrapCommand(tier)
	if !deployed.Present {
		return refusal.Refuse(refusal.CodeNotReady, "the %s tier has no Ocel bootstrap; run `%s`", tier, command)
	}
	for _, class := range account.BootstrapStorageClasses {
		if deployed.Buckets[class] == "" {
			return refusal.Refuse(refusal.CodeNotReady,
				"account bootstrap is present but its %s bucket is missing (a partial rollback?); re-run `%s`", class, command)
		}
	}
	for _, missing := range []struct {
		value string
		what  string
	}{
		{deployed.StateTable, "state table"},
		{deployed.VariablesTable, "variable store"},
	} {
		if missing.value == "" {
			return refusal.Refuse(refusal.CodeNotReady,
				"account bootstrap is present but its %s is missing (a partial rollback?); re-run `%s`", missing.what, command)
		}
	}
	return nil
}

func (p *Provider) transformPass() transform.Pass {
	if len(p.transforms) == 0 {
		return nil
	}
	return deploy.NodePass(p.projectDir, p.transforms)
}

func tableARN(region, account, table string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", region, account, table)
}

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

func (p *Provider) openEdge(kind edge.Kind) (edge.Edge, error) {
	if kind == edge.None {
		return nil, nil
	}
	return p.Edges().Open(kind, nil)
}
