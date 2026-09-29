package aws

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
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
	account, err := p.accountID(ctx)
	if err != nil {
		return deploy.Config{}, err
	}
	store := envvars.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	referenced, err := store.ReferenceOwners(ctx, envvars.Scope{Project: scope.Slug, Tier: scope.Tier})
	if err != nil {
		return deploy.Config{}, err
	}

	cfg := deploy.Config{
		Region:        p.aws.Region,
		BackendURL:    stateBackendURL(deployed.StateBucket, scope.Slug),
		Passphrase:    params.Passphrase,
		PulumiProject: naming.PulumiProject(scope.Slug),
		Secrets:       secretsmanager.NewFromConfig(p.aws),

		Tags:      &tagclock.Table{Dynamo: dynamodb.NewFromConfig(p.aws), Table: deployed.StateTable},
		KeyValues: p.KeyValues(),
		Rules:     elasticloadbalancingv2.NewFromConfig(p.aws),

		Tier:           scope.Tier,
		Slug:           scope.Slug,
		Env:            scope.Env,
		StateTable:     deployed.StateTable,
		StateTableARN:  tableARN(p.aws.Region, account, deployed.StateTable),
		VarsTable:      deployed.VarsTable,
		VarsTableARN:   tableARN(p.aws.Region, account, deployed.VarsTable),
		VarsKeyARN:     deployed.VarsKeyARN,
		AppBoundaryARN: deployed.AppBoundaryARN,
		VarsReferenced: referenced,

		RuntimeLayers: deployed.RuntimeLayers,

		ArtifactRoot:       appbuild.ArtifactRoot(p.projectDir),
		ArtifactBucket:     deployed.ArtifactBucket,
		AssetBucket:        deployed.AssetBucket,
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,

		CacheStoreBucket:  params.CacheStore.Bucket,
		CacheStoreObjects: cacheStoreObjects(params.CacheStore),

		Objects:     s3.NewFromConfig(p.aws),
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

		OriginSecret:         params.OriginSecret.Current,
		PreviousOriginSecret: params.OriginSecret.Previous,

		Transform: p.transformPass(),
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

func (p *Provider) requireBootstrapped(deployed bootstrap.Deployed, tier environment.Tier) error {
	command := provider.BootstrapCommand(tier)
	for _, missing := range []struct {
		value string
		what  string
	}{
		{deployed.StateBucket, "state bucket"},
		{deployed.ArtifactBucket, "artifact bucket"},
		{deployed.AssetBucket, "asset bucket"},
		{deployed.StateTable, "state table"},
		{deployed.VarsTable, "variable store"},
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

func cacheStoreObjects(store bootstrap.CacheStore) payloads.ObjectStore {
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
