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

	ec2 "github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
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

	// SessionTable is the account-global upload-sessions table bootstrap
	// provisions; a bucket's runtime and listener roles are scoped to it.
	SessionTable string
	// SessionTableARN is that table's ARN, used to scope the bucket IAM grants.
	SessionTableARN string
	// ListenerCodePath is the built listener-Lambda handler archive registerBucket
	// deploys. Packaging it (building + zipping the handler binary) rides the
	// provider's distribution workflow, which is deferred with provider publish;
	// it is threaded here so the deploy path is complete once that lands.
	ListenerCodePath string

	// Lifecycle is the environment lifecycle this deploy realizes under; it
	// selects each resource's realization (see realizationFor). Unspecified for
	// production and persistent previews, LIFECYCLE_EPHEMERAL for ephemeral
	// previews.
	Lifecycle providerv1.Environment_Lifecycle
	// Identity is the environment identity, used to name an ephemeral preview's
	// logical database slices. Empty for production.
	Identity string
	// SharedClusterEndpoint and SharedClusterSecretARN address the shared
	// preview cluster an ephemeral postgres slice is carved from (from the
	// preview bootstrap outputs). Empty outside ephemeral previews.
	SharedClusterEndpoint  string
	SharedClusterSecretARN string
}

// Run provisions every resource in manifest against AWS and returns the
// whole-stack connection outputs. progress reports discrete steps and log
// forwards Pulumi engine output; both may be nil. Run performs the real
// Pulumi up and is not exercised by unit tests.
func Run(ctx context.Context, cfg Config, manifest *providerv1.Manifest, progress, log func(string)) ([]*providerv1.ResourceOutput, error) {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
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
		for _, r := range manifest.GetResources() {
			switch {
			case r.GetPostgres() != nil:
				if realizationFor(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, cfg.Lifecycle) == RealizationLogicalSlice {
					if err := registerPostgresLogicalSlice(pctx, r.GetLogicalName(), postgresSliceArgs{
						DatabaseName:    sliceDatabaseName(cfg.Identity, r.GetLogicalName()),
						ClusterEndpoint: cfg.SharedClusterEndpoint,
						AdminSecretARN:  cfg.SharedClusterSecretARN,
					}); err != nil {
						return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
					}
					break
				}
				if err := registerPostgres(pctx, r.GetLogicalName(), translatePostgres(r.GetPostgres()), vpc.Id, vpc.CidrBlock, subnets.Ids); err != nil {
					return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
				}
			case r.GetBucket() != nil:
				if err := registerBucket(pctx, r.GetLogicalName(), translateBucket(r.GetBucket()), cfg.SessionTable, cfg.SessionTableARN, cfg.ListenerCodePath); err != nil {
					return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
				}
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
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", cfg.StackName, err)
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
	return collectOutputs(ctx, cfg.Secrets, manifest, res.Outputs)
}

// collectOutputs turns the stack's raw Pulumi outputs into typed
// per-resource ResourceOutputs, resolving each postgres resource's
// RDS-managed secret to a plaintext password.
func collectOutputs(ctx context.Context, secrets SecretsReader, manifest *providerv1.Manifest, outputs auto.OutputMap) ([]*providerv1.ResourceOutput, error) {
	var result []*providerv1.ResourceOutput
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
			out *providerv1.ResourceOutput
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
	return result, nil
}

func collectPostgresOutput(ctx context.Context, secrets SecretsReader, name string, fields map[string]interface{}) (*providerv1.ResourceOutput, error) {
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

	return &providerv1.ResourceOutput{
		LogicalName: name,
		Output: &providerv1.ResourceOutput_Postgres{
			Postgres: &providerv1.PostgresOutput{
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
