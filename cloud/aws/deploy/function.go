package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	secretsmanager "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

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
	defaultMembraneLayerARN = "arn:aws:lambda:us-east-1:363236815301:layer:ocel-membrane:6"
	membraneLayerARNEnv     = "OCEL_MEMBRANE_LAYER_ARN"

	// A function in the manifest is web-facing (an express framework implies
	// it), so its Function URL is public this iteration — no IAM in front.
	functionURLAuthNone = "NONE"

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
	Runtime string
	Handler string
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
	return functionArgs{Runtime: runtime, Handler: handler}
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

// registerFunction realizes one ManifestFunction as an AWS Lambda from its
// `.func` artifact plus a public Function URL, with env carrying every
// manifest resource (env). artifact points at the S3 object the provider
// already uploaded the `.func` deployment package to; its content-addressed key
// changes when the code changes, so Pulumi redeploys exactly the changed
// functions. The Function URL is exported under logicalName for
// collectFunctionOutput.
func registerFunction(ctx *pulumi.Context, logicalName string, args functionArgs, artifact artifactRef, env pulumi.StringMap) error {
	role, err := newServiceRole(ctx, logicalName+"-fn", "lambda.amazonaws.com", nil)
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, logicalName+"-fn-logs", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return err
	}

	// The lambdanode bootstrap (in the membrane layer) takes over as the runtime and
	// imports the user entrypoint at /var/task/<handler>. The Lambda's own
	// Handler config is vestigial under this exec wrapper.
	env["AWS_LAMBDA_EXEC_WRAPPER"] = pulumi.String(execWrapper)
	env["OCEL_HANDLER"] = pulumi.String("/var/task/" + args.Handler)

	fn, err := lambda.NewFunction(ctx, logicalName, &lambda.FunctionArgs{
		Runtime:  pulumi.String(args.Runtime),
		Handler:  pulumi.String(lambdaConfigHandler),
		Role:     role.Arn,
		S3Bucket: pulumi.String(artifact.Bucket),
		S3Key:    pulumi.String(artifact.Key),
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
