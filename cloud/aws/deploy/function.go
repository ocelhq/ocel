package deploy

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	secretsmanager "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Provider-chosen defaults for a realized function. A ManifestFunction always
// carries a runtime and a handler (the app builder emits both), but an empty
// value falls back to the pinned Node runtime and the conventional entrypoint.
const (
	defaultFunctionRuntime = "nodejs24.x"

	// The manifest handler is the user entrypoint's path within the .func (e.g.
	// `src/server.js`); the lambdanode entrypoint imports it via OCEL_HANDLER. This is
	// the fallback when the manifest omits it.
	defaultFunctionEntry = "src/server.js"

	// AWS's implicit 3s/128MB cannot fit an SSR cold start, and a Lambda
	// timeout surfaces as neither a body nor an error — only a REPORT line —
	// so an undersized function reads as a hang rather than a failure. Memory
	// is the CPU dial too: Lambda scales cores with it, so 128MB is what makes
	// a cold start slow enough to hit the ceiling in the first place.
	defaultFunctionMemoryMB       = 1024
	defaultFunctionTimeoutSeconds = 30

	// lambdaConfigHandler is the Lambda's own Handler config value. Under the
	// lambdanode exec-wrapper the Go bootstrap owns the Runtime API loop, so
	// this is vestigial — but the managed nodejs runtime still requires a
	// syntactically valid value.
	lambdaConfigHandler = "index.handler"

	// execWrapper points the managed runtime at the lambdanode Go bootstrap shipped
	// in the membrane layer; it takes over the Runtime API loop.
	execWrapper = "/opt/ocel/bootstrap"

	// defaultMembraneLayerARN pins the Ocel-owned, publicly-shared membrane
	// layer version. It is a released-artifact version, bumped only when the
	// layer is republished (`make publish-layer`); override via
	// OCEL_MEMBRANE_LAYER_ARN for dev/testing.
	defaultMembraneLayerARN = "arn:aws:lambda:us-east-1:363236815301:layer:ocel-membrane:11"
	membraneLayerARNEnv     = "OCEL_MEMBRANE_LAYER_ARN"

	// A function in the manifest is web-facing (an express framework implies
	// it), so its Function URL is public this iteration — no IAM in front.
	functionURLAuthNone = "NONE"

	// functionURLInvokeModeStream deploys every Function URL in response-stream
	// mode: the service invokes via InvokeWithResponseStream and the lambdanode
	// bootstrap replies with the http-integration-response streaming contract.
	// All functions stream (streaming is a superset — small responses stream
	// fine), so this is unconditional.
	functionURLInvokeModeStream = "RESPONSE_STREAM"

	// outputKeyFunctionURL is the key registerFunction exports the Function URL
	// under, read back by collectFunctionOutput.
	outputKeyFunctionURL = "url"
)

// membraneLayerARN is the membrane layer version deployed functions attach,
// taken from OCEL_MEMBRANE_LAYER_ARN when set, else the pinned default.
func membraneLayerARN() string {
	if arn := os.Getenv(membraneLayerARNEnv); arn != "" {
		return arn
	}
	return defaultMembraneLayerARN
}

// functionArgs is the fully-resolved set of arguments a ManifestFunction lowers
// to, independent of any Pulumi or AWS call. It is the pure output of
// translateFunction so the translation can be unit-tested without provisioning.
type functionArgs struct {
	Runtime        string
	Handler        string
	MemorySizeMB   int
	TimeoutSeconds int
}

// isrConfig points a Next function's cache handler at the account-global stores
// backing ISR. Prefix and TagNamespace both derive from the deploy's
// <env>/<project>/<app>/<build> identity, so an app can only ever address its
// own entries and its own tags — which is what isrPolicy then enforces.
type isrConfig struct {
	Bucket   string
	Prefix   string
	Table    string
	TableARN string

	// CacheStoreParam and CacheStoreParamARN address the SSM parameter holding
	// the substrate's adopted cache store, which the membrane reads at init and
	// injects into node. The deploy resolves the substrate class, so the runtime
	// is handed a parameter name rather than a class to map — one mapping, on the
	// side that also has to name the parameter in the role's grant. Set whether or
	// not a store was actually adopted: an unadopted one is signalled by the
	// parameter not existing, which the membrane reads as "stay on S3".
	CacheStoreParam    string
	CacheStoreParamARN string
}

// tagNamespace is the partition-key prefix this app's ISR tag records live
// under in the shared state table. It mirrors the S3 prefix so one identity
// governs both stores. Building it from the same Prefix is what lets isrPolicy
// scope DynamoDB with a single LeadingKeys wildcard.
func (c isrConfig) tagNamespace() string {
	return "TAG#" + strings.ReplaceAll(c.Prefix, "/", "#") + "#"
}

// env is what the bundled cache handler reads to find its backing stores.
func (c isrConfig) env() map[string]string {
	env := map[string]string{
		"OCEL_ISR_BUCKET":        c.Bucket,
		"OCEL_ISR_PREFIX":        c.Prefix,
		"OCEL_STATE_TABLE":       c.Table,
		"OCEL_STATE_TABLE_INDEX": bootstrap.StateTableIndexName,
		"OCEL_ISR_TAG_NAMESPACE": c.tagNamespace(),
	}
	// Left unset rather than set empty when there is no parameter: the membrane
	// reads an unset name as "this substrate adopted no cache store" and skips
	// the fetch entirely, which is what keeps an older substrate on S3.
	if c.CacheStoreParam != "" {
		env["OCEL_CACHE_STORE_PARAM"] = c.CacheStoreParam
	}
	return env
}

// isrPolicy grants a Next function exactly the cache access it needs and no
// more. Both the asset bucket and the state table are account-global and shared
// by every env, project and app, so an unscoped grant would let any function
// read or corrupt another project's cache — and the state table also holds
// upload sessions, whose items carry HMAC secrets. The DynamoDB grant leans on
// StringLike (plain LeadingKeys matching is exact-only) to bound the function to
// its own tag partitions.
func isrPolicy(c isrConfig) (string, error) {
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"s3:GetObject", "s3:PutObject"},
			"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", c.Bucket, c.Prefix),
		},
		map[string]any{
			"Effect": "Allow",
			// Exactly the calls the handler's tag store makes against the
			// table itself: readTags sends BatchGetItem, writeTags sends
			// UpdateItem (it merges, so PutItem would clobber an earlier
			// expiry). Index reads are granted separately below. Adding a
			// call in either place means adding its action here — a mismatch
			// 403s at runtime, and revalidateTag does not catch, so it throws
			// out of the user's server action.
			"Action":   []string{"dynamodb:BatchGetItem", "dynamodb:UpdateItem"},
			"Resource": c.TableARN,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{
					"dynamodb:LeadingKeys": []string{c.tagNamespace() + "*"},
				},
			},
		},
		map[string]any{
			"Effect": "Allow",
			// An index is not covered by its table's ARN, so the tag sync's
			// Query needs this separate resource or it 403s. LeadingKeys is
			// evaluated against the *index's* partition key here, which is
			// the tag namespace verbatim — the same wildcard admits it
			// because * matches zero characters.
			"Action":   []string{"dynamodb:Query"},
			"Resource": c.TableARN + "/index/" + bootstrap.StateTableIndexName,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{
					"dynamodb:LeadingKeys": []string{c.tagNamespace() + "*"},
				},
			},
		},
	}

	// The membrane reads this parameter at init to find the store its cache
	// handler writes to. Without the grant the read 403s, and a 403 fails the
	// init rather than degrading — so the grant and the injected parameter name
	// have to appear together, which is why both hang off the same field.
	if c.CacheStoreParamARN != "" {
		statements = append(statements,
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"ssm:GetParameter"},
				"Resource": c.CacheStoreParamARN,
			},
			map[string]any{
				"Effect": "Allow",
				// The parameter is a SecureString, so reading it decrypts under
				// the account's default SSM key, whose ARN the deploy does not
				// know. The encryption-context condition is what scopes this to
				// the one parameter rather than to every secret in the account:
				// SSM puts the parameter's ARN into the encryption context of
				// every decrypt it makes, so a Resource of "*" cannot be
				// exercised against anything else.
				"Action":   []string{"kms:Decrypt"},
				"Resource": "*",
				"Condition": map[string]any{
					"StringEquals": map[string]any{
						"kms:EncryptionContext:PARAMETER_ARN": c.CacheStoreParamARN,
					},
				},
			},
		)
	}

	doc := map[string]any{"Version": "2012-10-17", "Statement": statements}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render isr policy: %w", err)
	}
	return string(out), nil
}

// translateFunction lowers a ManifestFunction into the concrete Lambda
// arguments the provider provisions. Empty runtime/handler fall back to the
// pinned Node defaults. Handler is the user entrypoint path OCEL_HANDLER
// resolves as /var/task/<handler>.
func translateFunction(fn *deploymentsv1.ManifestFunction) functionArgs {
	runtime := defaultFunctionRuntime
	if r := fn.GetRuntime(); r != "" {
		runtime = r
	}
	handler := defaultFunctionEntry
	if h := fn.GetHandler(); h != "" {
		handler = h
	}
	return functionArgs{
		Runtime:        runtime,
		Handler:        handler,
		MemorySizeMB:   defaultFunctionMemoryMB,
		TimeoutSeconds: defaultFunctionTimeoutSeconds,
	}
}

// functionEnvKey is the environment variable a resource is injected onto every
// function under: OCEL_RESOURCE_<TYPE>_<id>, where <TYPE> is the resource
// type's canonical uppercase token and <id> is the resource's user id. It
// matches exactly what the SDK reads (get-config.ts).
func functionEnvKey(rt resourcesv1.ResourceType, id string) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", resourceTypeToken(rt), id)
}

// resourceTypeToken is a resource type's canonical uppercase token, the middle
// segment of its OCEL_RESOURCE_<TYPE>_<id> env key.
func resourceTypeToken(rt resourcesv1.ResourceType) string {
	switch rt {
	case resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES:
		return "POSTGRES"
	case resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:
		return "BUCKET"
	default:
		return "UNSPECIFIED"
	}
}

// postgresEnvPayload is the OCEL_RESOURCE_POSTGRES_<id> value the SDK reads
// (pg.ts): a JSON object carrying the connection string.
func postgresEnvPayload(username, password, host string, port int, database string) string {
	conn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", username, password, host, port, database)
	b, _ := json.Marshal(map[string]string{"connectionString": conn})
	return string(b)
}

// bucketEnvPayload is the OCEL_RESOURCE_BUCKET_<id> value the SDK reads
// (bucket.ts): a JSON object pointing at the BucketService endpoint (address)
// and the provisioned bucket binding.
func bucketEnvPayload(address, bucket string) string {
	b, _ := json.Marshal(map[string]string{"address": address, "bucket": bucket})
	return string(b)
}

// artifactArchivePath resolves a ManifestFunction.artifact_path (relative to
// the project's .ocel/output) against the deploy's artifact root, giving the
// absolute path to the `.func` directory the provider hashes, zips, and uploads
// to the artifact bucket before provisioning.
func artifactArchivePath(root, artifactPath string) string {
	return filepath.Join(root, artifactPath)
}

// collectFunctionOutput builds the ResourceOutput reporting a realized
// function's web-facing URL, keyed by the function's logical_name.
func collectFunctionOutput(logicalName, url string) *deploymentsv1.ResourceOutput {
	return &deploymentsv1.ResourceOutput{
		LogicalName: logicalName,
		Output: &deploymentsv1.ResourceOutput_Function{
			Function: &deploymentsv1.FunctionOutput{Url: url},
		},
	}
}

// executionRole is one app's Lambda execution role: the app it belongs to and
// the ISR cache it grants, nil when the app keeps none.
type executionRole struct {
	App   string
	Cache *isrConfig
}

// executionRoles is the set of roles a manifest needs — one per app, in
// first-appearance order so redeploys declare them identically. A role's grant
// is its own app's cache and nothing else, which is what keeps one app's
// functions out of another app's cached pages.
func executionRoles(caches map[string]*isrConfig, functions []*deploymentsv1.ManifestFunction) []executionRole {
	var roles []executionRole
	seen := map[string]bool{}
	for _, fn := range functions {
		app := fn.GetApp()
		if seen[app] {
			continue
		}
		seen[app] = true
		roles = append(roles, executionRole{App: app, Cache: caches[app]})
	}
	return roles
}

// newFunctionRole creates the IAM role every Lambda belonging to one app
// assumes: the CloudWatch Logs grant every function needs, plus the app's own
// ISR cache grant when it has one.
func newFunctionRole(ctx *pulumi.Context, r executionRole) (*iam.Role, error) {
	name := "ocel-fn-" + safeName(r.App)
	role, err := newServiceRole(ctx, name, "lambda.amazonaws.com", nil)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, name+"-logs", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return nil, err
	}
	if r.Cache != nil {
		policy, err := isrPolicy(*r.Cache)
		if err != nil {
			return nil, err
		}
		if _, err := iam.NewRolePolicy(ctx, name+"-isr", &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	return role, nil
}

// registerFunction realizes one ManifestFunction as an AWS Lambda from its
// `.func` artifact plus a public Function URL, with env carrying every
// manifest resource (env). The function assumes its own app's execution role
// (roleArn, from newFunctionRole). artifact points at the S3 object the provider
// already uploaded the `.func` deployment package to; its content-addressed key
// changes when the code changes, so Pulumi redeploys exactly the changed
// functions. isr is the app's cache, nil when it keeps none; it injects the
// cache handler's env, and the grant backing it lives on that same app's role.
// The Function URL is exported under logicalName for collectFunctionOutput.
func registerFunction(ctx *pulumi.Context, logicalName string, args functionArgs, artifact artifactRef, env pulumi.StringMap, isr *isrConfig, roleArn pulumi.StringInput) error {
	// env is shared across every function in the deploy, so per-function
	// additions are made on a copy.
	env = maps.Clone(env)

	// The lambdanode bootstrap (in the membrane layer) takes over as the runtime and
	// imports the user entrypoint at /var/task/<handler>. The Lambda's own
	// Handler config is vestigial under this exec wrapper.
	env["AWS_LAMBDA_EXEC_WRAPPER"] = pulumi.String(execWrapper)
	env["OCEL_HANDLER"] = pulumi.String("/var/task/" + args.Handler)

	if isr != nil {
		for k, v := range isr.env() {
			env[k] = pulumi.String(v)
		}
	}

	fn, err := lambda.NewFunction(ctx, logicalName, &lambda.FunctionArgs{
		Runtime:    pulumi.String(args.Runtime),
		Handler:    pulumi.String(lambdaConfigHandler),
		Role:       roleArn,
		S3Bucket:   pulumi.String(artifact.Bucket),
		S3Key:      pulumi.String(artifact.Key),
		MemorySize: pulumi.Int(args.MemorySizeMB),
		Timeout:    pulumi.Int(args.TimeoutSeconds),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: env,
		},

		Layers: pulumi.StringArray{
			pulumi.String(membraneLayerARN()),
		},
	})
	if err != nil {
		return err
	}

	url, err := lambda.NewFunctionUrl(ctx, logicalName+"-url", &lambda.FunctionUrlArgs{
		FunctionName:      fn.Name,
		AuthorizationType: pulumi.String(functionURLAuthNone),
		InvokeMode:        pulumi.String(functionURLInvokeModeStream),
	})
	if err != nil {
		return err
	}

	// An auth-type NONE Function URL is only publicly invokable with a
	// resource-based policy granting public access. As of October 2025 AWS
	// requires BOTH lambda:InvokeFunctionUrl and lambda:InvokeFunction on the
	// policy; without both, unauthenticated requests get a 403.
	if _, err := lambda.NewPermission(ctx, logicalName+"-url-invoke", &lambda.PermissionArgs{
		Action:              pulumi.String("lambda:InvokeFunctionUrl"),
		Function:            fn.Name,
		Principal:           pulumi.String("*"),
		FunctionUrlAuthType: pulumi.String(functionURLAuthNone),
	}); err != nil {
		return err
	}
	// The second required grant, scoped with lambda:InvokedViaFunctionUrl so the
	// function is only publicly invokable through its URL, not the plain Invoke
	// API.
	if _, err := lambda.NewPermission(ctx, logicalName+"-invoke", &lambda.PermissionArgs{
		Action:                pulumi.String("lambda:InvokeFunction"),
		Function:              fn.Name,
		Principal:             pulumi.String("*"),
		InvokedViaFunctionUrl: pulumi.Bool(true),
	}); err != nil {
		return err
	}

	ctx.Export(logicalName, pulumi.Map{outputKeyFunctionURL: url.FunctionUrl})
	return nil
}

// postgresEnvValue composes the OCEL_RESOURCE_POSTGRES_<id> value from a
// provisioned postgres resource's live outputs: the RDS-managed master
// password is read from its Secrets Manager secret (a Pulumi data source, so
// the Lambda depends on the secret and transitively the cluster), then folded
// into the SDK connection-string payload.
func postgresEnvValue(ctx *pulumi.Context, username, host pulumi.StringInput, port pulumi.IntInput, database string, secretARN pulumi.StringInput) pulumi.StringOutput {
	secret := secretsmanager.LookupSecretVersionOutput(ctx, secretsmanager.LookupSecretVersionOutputArgs{
		SecretId: secretARN,
	}).SecretString()
	return pulumi.All(username, host, port, secret).ApplyT(func(vs []interface{}) (string, error) {
		user, _ := vs[0].(string)
		h, _ := vs[1].(string)
		p, _ := vs[2].(int)
		password, err := parseManagedPassword(vs[3].(string))
		if err != nil {
			return "", err
		}
		return postgresEnvPayload(user, password, h, p, database), nil
	}).(pulumi.StringOutput)
}

// bucketEnvValue composes the OCEL_RESOURCE_BUCKET_<id> value from a
// provisioned bucket's name and the BucketService endpoint. The address is
// the deferred placeholder the bucket output already uses (see
// deferredRuntimeAddress) until the membrane lands.
func bucketEnvValue(bucket pulumi.StringInput) pulumi.StringOutput {
	return bucket.ToStringOutput().ApplyT(func(b string) string {
		return bucketEnvPayload(deferredRuntimeAddress, b)
	}).(pulumi.StringOutput)
}

// parseManagedPassword extracts the password from an RDS-managed master-user
// secret's JSON string ({username, password}).
func parseManagedPassword(secretJSON string) (string, error) {
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(secretJSON), &parsed); err != nil {
		return "", fmt.Errorf("parse managed secret: %w", err)
	}
	return parsed.Password, nil
}
