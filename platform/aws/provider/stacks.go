package provider

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
)

const artifactRootDirName = constants.ProjectStateDirName + "/output"

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
	store := values.Store{Records: p.Records(), Cipher: p.Cipher()}
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

		Tags:    &tagclock.Table{Dynamo: dynamodb.NewFromConfig(p.aws), Table: held.StateTable},
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

func (p *Provider) transformPass(root string) transformkit.Pass {
	if len(p.transforms) == 0 {
		return nil
	}
	return deploy.NodePass(root, p.transforms)
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

func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
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
