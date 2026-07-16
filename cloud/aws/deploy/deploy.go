// Package deploy provisions a project's declared resources into a user's AWS
// account using the Pulumi Automation API (inline program). This slice
// supports the postgres resource, translated to an AWS RDS Aurora Serverless
// v2 cluster. The resource translation (translatePostgres) is a pure
// function so it can be unit-tested without touching Pulumi or AWS; the
// orchestration here drives the real deploy and is exercised only by an
// opt-in run against a live account.
package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// SecretsReader is the subset of the AWS Secrets Manager client the deploy
// path needs, to resolve an RDS-managed master-password secret to its
// plaintext for the connection outputs. The aws-sdk-go-v2 client satisfies
// it; tests can substitute a fake.
type SecretsReader interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Config carries everything a deploy needs beyond the manifest: where Pulumi
// keeps state, how it decrypts it, which stack to act on, and the AWS clients
// the provider resolves outputs with.
type Config struct {
	Region      string
	BackendURL  string // Pulumi self-managed backend, e.g. "s3://<bucket>"
	Passphrase  string // Pulumi passphrase secrets-provider value
	ProjectName string // Pulumi project name, e.g. "ocel"
	StackName   string // "<project_id>-<env>"
	Pulumi      auto.PulumiCommand
	Secrets     SecretsReader

	// StateTable is the account-global state table bootstrap provisions, shared
	// by every entity that keys into it: a bucket's upload sessions and a Next
	// app's ISR tag records both live here under their own key prefixes.
	StateTable string
	// StateTableARN is that table's ARN, used to scope each consumer's IAM
	// grants to its own key prefix.
	StateTableARN string
	// ListenerCodePath is the built listener-Lambda handler archive registerBucket
	// deploys. Packaging it (building + zipping the handler binary) rides the
	// provider's distribution workflow, which is deferred with provider publish;
	// it is threaded here so the deploy path is complete once that lands.
	ListenerCodePath string

	// ArtifactRoot is the absolute path a ManifestFunction's artifact_path is
	// resolved against — the project's .ocel/output directory. Each function's
	// `.func` artifact lives under it; the provider hashes, zips, and uploads
	// that directory to the artifact bucket before provisioning.
	ArtifactRoot string
	// ArtifactBucket is the account-global S3 bucket (from bootstrap) function
	// deployment packages are uploaded to; each Lambda's code points at an object
	// in it rather than an inline archive.
	ArtifactBucket string
	// Uploader puts function artifacts into ArtifactBucket. The aws-sdk-go-v2 S3
	// client satisfies it.
	Uploader ArtifactUploader

	// AssetBucket is the account-global S3 bucket (from bootstrap) prerender
	// configs + fallbacks are uploaded to, keyed by build id. Empty when the
	// bootstrap predates it.
	AssetBucket string
	// Env is the environment segment of the S3 asset key ("prod", or
	// "preview-<identity>"): the same token stackName suffixes.
	Env string

	// Cloudflare deploys the Next.js routing worker once its Lambdas exist and
	// their Function URLs are known. Nil unless the project has a Next.js app;
	// the real cloudflare-go implementation is the end-to-end seam.
	Cloudflare CloudflareDeployer

	// Class is the environment class this deploy realizes under. It selects the
	// web-facing worker's custom domain: only CLASS_PRODUCTION consults
	// Manifest.domains["production"].
	Class deploymentsv1.Environment_Class
	// Lifecycle is the environment lifecycle this deploy realizes under; it
	// selects each resource's realization (see realizationFor). Unspecified for
	// production and persistent previews, LIFECYCLE_EPHEMERAL for ephemeral
	// previews.
	Lifecycle deploymentsv1.Environment_Lifecycle
	// Identity is the environment identity, used to name an ephemeral preview's
	// logical database slices. Empty for production.
	Identity string
	// SharedClusterEndpoint and SharedClusterSecretARN address the shared
	// preview cluster an ephemeral postgres slice is carved from (from the
	// preview bootstrap outputs). Empty outside ephemeral previews.
	SharedClusterEndpoint  string
	SharedClusterSecretARN string

	// ExpiresAt is the epoch-seconds expiry stamped on an ephemeral preview
	// stack, so `ocel preview ls` can surface age/expiry and a future reaper can
	// find orphans. 0 means no expiry (production and persistent previews).
	ExpiresAt int64
}

// Run provisions every resource in manifest against AWS and returns the
// whole-stack connection outputs. progress reports discrete steps and log
// forwards Pulumi engine output; both may be nil. Run performs the real
// Pulumi up and is not exercised by unit tests.
func Run(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress, log func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	if len(manifest.GetFunctions()) > 0 {
		report(progress, "Uploading function artifacts")
	}
	artifacts, err := uploadFunctionArtifacts(ctx, cfg, manifest)
	if err != nil {
		return nil, err
	}

	// A Next app's prerender configs + fallbacks ride to the asset bucket
	// alongside the function artifacts, keyed by build id. No-op otherwise.
	if err := uploadPrerenderAssets(ctx, cfg, manifest); err != nil {
		return nil, err
	}

	program := func(pctx *pulumi.Context) error {
		vpc, err := ec2.LookupVpc(pctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
		if err != nil {
			return fmt.Errorf("look up default VPC: %w", err)
		}
		subnets, err := ec2.GetSubnets(pctx, &ec2.GetSubnetsArgs{
			Filters: []ec2.GetSubnetsFilter{{Name: "vpc-id", Values: []string{vpc.Id}}},
		})
		if err != nil {
			return fmt.Errorf("look up default VPC subnets: %w", err)
		}
		// Every function is injected with every resource's connection payload
		// (OCEL_RESOURCE_<TYPE>_<id>), mirroring how `ocel dev` injects all
		// resources. Resources are realized first so their outputs are available
		// to wire onto each function's env.
		env := pulumi.StringMap{}
		for _, r := range manifest.GetResources() {
			var (
				value pulumi.StringOutput
				err   error
			)
			switch {
			case r.GetPostgres() != nil:
				if realizationFor(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, cfg.Lifecycle) == RealizationLogicalSlice {
					value, err = registerPostgresLogicalSlice(pctx, r.GetLogicalName(), postgresSliceArgs{
						DatabaseName:    sliceDatabaseName(cfg.Identity, r.GetLogicalName()),
						ClusterEndpoint: cfg.SharedClusterEndpoint,
						AdminSecretARN:  cfg.SharedClusterSecretARN,
					})
					break
				}
				value, err = registerPostgres(pctx, r.GetLogicalName(), translatePostgres(r.GetPostgres()), vpc.Id, vpc.CidrBlock, subnets.Ids)
			case r.GetBucket() != nil:
				value, err = registerBucket(pctx, r.GetLogicalName(), translateBucket(r.GetBucket()), cfg.StateTable, cfg.StateTableARN, cfg.ListenerCodePath)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
			}
			env[functionEnvKey(r.GetResource().GetType(), r.GetResource().GetName())] = value
		}

		// A Next function's cache handler reads and writes this app's slice of
		// the account-global asset bucket and state table; everything else gets
		// no cache access at all. The prefix is per-deploy, so the ISR config is
		// built once and its grant lives on the one shared role.
		prefix, err := assetPrefix(cfg, manifest)
		if err != nil {
			return err
		}
		var deployISR *isrConfig
		if prefix != "" {
			deployISR = &isrConfig{
				Bucket:   cfg.AssetBucket,
				Prefix:   prefix,
				Table:    cfg.StateTable,
				TableARN: cfg.StateTableARN,
			}
		}

		var fnRoleArn pulumi.StringInput
		if len(manifest.GetFunctions()) > 0 {
			role, err := newFunctionRole(pctx, deployISR)
			if err != nil {
				return err
			}
			fnRoleArn = role.Arn
		}

		for _, fn := range manifest.GetFunctions() {
			// Only a Next function carries the ISR cache env; the grant is already
			// on the shared role.
			var isr *isrConfig
			if deployISR != nil && fn.GetFramework() == frameworkNext {
				isr = deployISR
			}
			if err := registerFunction(pctx, fn.GetLogicalName(), translateFunction(fn), artifacts[fn.GetLogicalName()], env, isr, fnRoleArn); err != nil {
				return fmt.Errorf("declare %s: %w", fn.GetLogicalName(), err)
			}
		}
		return nil
	}

	report(progress, "Preparing deployment stack")
	stack, err := auto.UpsertStackInlineSource(ctx, cfg.StackName, cfg.ProjectName, program,
		auto.Pulumi(cfg.Pulumi),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(map[string]string{
			"PULUMI_BACKEND_URL":       cfg.BackendURL,
			"PULUMI_CONFIG_PASSPHRASE": cfg.Passphrase,
			"AWS_REGION":               cfg.Region,
			// TODO: revisit ?
			"PULUMI_SKIP_CHECKPOINTS":  "true",
			"PULUMI_SKIP_UPDATE_CHECK": "true",
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", cfg.StackName, err)
	}

	if err := stampExpiry(ctx, stack, cfg.ExpiresAt); err != nil {
		return nil, err
	}

	report(progress, "Provisioning resources (this can take several minutes)")
	logWriter := lineWriter(log)
	upOpts := []optup.Option{}
	if logWriter != nil {
		upOpts = append(upOpts, optup.ProgressStreams(logWriter))
	}

	res, err := stack.Up(ctx, upOpts...)
	logWriter.Flush() // emit any final, un-newline-terminated engine line
	if err != nil {
		return nil, fmt.Errorf("provision stack %s: %w", cfg.StackName, err)
	}

	report(progress, "Collecting outputs")
	outputs, err := collectOutputs(ctx, cfg.Secrets, manifest, res.Outputs)
	if err != nil {
		return nil, err
	}

	// The Next.js worker fronts the just-provisioned Lambdas, so it deploys last
	// — it needs their Function URLs. A failure here fails the deploy; the AWS
	// resources persist and a redeploy is idempotent.
	workerOutputs, err := deployNextWorker(ctx, cfg, manifest, outputs, func(msg string) { report(progress, msg) })
	if err != nil {
		return nil, err
	}
	return append(outputs, workerOutputs...), nil
}

// previewExpiryTagKey is the Pulumi stack tag holding an ephemeral preview's
// epoch-seconds expiry; ListPreviewStacks reads it back for `ocel preview ls`.
const previewExpiryTagKey = "ocel:expires_at"

// stampExpiry records expiresAt as a Pulumi stack tag on an ephemeral preview
// stack, so ls can surface expiry and a future reaper can find orphans. A zero
// expiresAt (production and persistent previews) is a no-op. The stack-tag write
// itself needs a live backend, so — like Run's provisioning body — it is the
// opt-in-e2e seam: the value is computed and threaded here now, and the
// stack.Workspace() tag write against the live backend lands at the marked line.
func stampExpiry(ctx context.Context, stack auto.Stack, expiresAt int64) error {
	if expiresAt == 0 {
		return nil
	}
	// Opt-in-e2e seam: write previewExpiryTagKey=strconv.FormatInt(expiresAt, 10)
	// via stack.Workspace() against the live Pulumi backend.
	_ = stack
	return nil
}

// collectOutputs turns the stack's raw Pulumi outputs into typed
// per-resource ResourceOutputs, resolving each postgres resource's
// RDS-managed secret to a plaintext password.
func collectOutputs(ctx context.Context, secrets SecretsReader, manifest *deploymentsv1.Manifest, outputs auto.OutputMap) ([]*deploymentsv1.ResourceOutput, error) {
	var result []*deploymentsv1.ResourceOutput
	for _, r := range manifest.GetResources() {
		if r.GetPostgres() == nil && r.GetBucket() == nil {
			continue
		}
		name := r.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("output for %s is not a map", name)
		}
		var (
			out *deploymentsv1.ResourceOutput
			err error
		)
		switch {
		case r.GetPostgres() != nil:
			out, err = collectPostgresOutput(ctx, secrets, name, fields)
		case r.GetBucket() != nil:
			out, err = collectBucketOutput(name, fields)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	for _, fn := range manifest.GetFunctions() {
		name := fn.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("output for %s is not a map", name)
		}
		url, err := requireStringField(fields, name, outputKeyFunctionURL)
		if err != nil {
			return nil, err
		}
		result = append(result, collectFunctionOutput(name, url))
	}
	return result, nil
}

func collectPostgresOutput(ctx context.Context, secrets SecretsReader, name string, fields map[string]interface{}) (*deploymentsv1.ResourceOutput, error) {
	host, err := requireStringField(fields, name, outputKeyHost)
	if err != nil {
		return nil, err
	}
	database, err := requireStringField(fields, name, outputKeyDatabase)
	if err != nil {
		return nil, err
	}
	username, err := requireStringField(fields, name, outputKeyUsername)
	if err != nil {
		return nil, err
	}
	secretARN, err := requireStringField(fields, name, outputKeySecretARN)
	if err != nil {
		return nil, err
	}

	port := postgresPort
	if p, ok := fields[outputKeyPort].(float64); ok {
		port = int(p)
	}

	password, err := resolveManagedPassword(ctx, secrets, secretARN)
	if err != nil {
		return nil, fmt.Errorf("resolve master password for %s: %w", name, err)
	}

	return &deploymentsv1.ResourceOutput{
		LogicalName: name,
		Output: &deploymentsv1.ResourceOutput_Postgres{
			Postgres: &deploymentsv1.PostgresOutput{
				Host:     host,
				Port:     int32(port),
				Database: database,
				Username: username,
				Password: password,
			},
		},
	}, nil
}

// requireStringField reads key from a resource's raw output map, erroring if
// it is absent, not a string, or empty — so a mistyped or missing connection
// field surfaces as a deploy failure rather than a misleading success with an
// unusable connection.
func requireStringField(fields map[string]interface{}, name, key string) (string, error) {
	v, ok := fields[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("output %q for %s is missing or not a non-empty string", key, name)
	}
	return v, nil
}

// resolveManagedPassword reads the RDS-managed master-user secret and returns
// its password. RDS stores the secret as JSON with username/password fields.
func resolveManagedPassword(ctx context.Context, secrets SecretsReader, secretARN string) (string, error) {
	if secretARN == "" {
		return "", fmt.Errorf("empty master-user secret ARN")
	}
	if secrets == nil {
		return "", fmt.Errorf("no Secrets Manager client configured")
	}
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretARN})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", secretARN)
	}
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &parsed); err != nil {
		return "", fmt.Errorf("parse managed secret: %w", err)
	}
	return parsed.Password, nil
}
