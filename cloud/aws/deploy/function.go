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

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const (
	defaultFunctionRuntime = "nodejs24.x"

	defaultFunctionEntry = "src/server.js"

	defaultFunctionMemoryMB       = 1024
	defaultFunctionTimeoutSeconds = 30

	nextBundleFunctionMemoryMB = 1769

	lambdaConfigHandler = "index.handler"

	execWrapper = "/opt/ocel/bootstrap"

	defaultMembraneLayerARN = "arn:aws:lambda:us-east-1:363236815301:layer:ocel-membrane:29"
	membraneLayerARNEnv     = "OCEL_MEMBRANE_LAYER_ARN"

	bytecodeCacheEnv = "OCEL_BYTECODE_CACHE"

	bytecodeEmbedEnv = "OCEL_BYTECODE_EMBED"

	functionURLAuthIAM = "AWS_IAM"

	tagApp     = "ocel:app"
	tagEnv     = "ocel:env"
	tagProject = "ocel:project"

	functionURLInvokeModeStream = "RESPONSE_STREAM"

	outputKeyFunctionURL = "url"

	outputKeyFunctionName = "functionName"
)

func membraneLayerARN() string {
	if arn := os.Getenv(membraneLayerARNEnv); arn != "" {
		return arn
	}
	return defaultMembraneLayerARN
}

func bytecodeCacheEnabled() bool {
	return os.Getenv(bytecodeCacheEnv) == "1"
}

func bytecodeEmbedRequested() bool {
	return os.Getenv(bytecodeEmbedEnv) == "1"
}

func bytecodeEmbedEnabled() bool {
	return bytecodeEmbedRequested() && bytecodeCacheEnabled()
}

type functionArgs struct {
	Runtime        string
	Handler        string
	MemorySizeMB   int
	TimeoutSeconds int
}

type isrConfig struct {
	Bucket   string
	Prefix   string
	Table    string
	TableARN string

	CacheStoreBucket string

	WriterURL    string
	WriterSecret string
}

func (c isrConfig) tagNamespace() string {
	return "TAG#" + strings.ReplaceAll(c.Prefix, "/", "#") + "#"
}

func (c isrConfig) env() map[string]string {
	env := map[string]string{
		"OCEL_ISR_BUCKET":        c.Bucket,
		"OCEL_ISR_PREFIX":        c.Prefix,
		"OCEL_STATE_TABLE":       c.Table,
		"OCEL_ISR_TAG_NAMESPACE": c.tagNamespace(),
	}
	if c.CacheStoreBucket != "" {
		env["OCEL_ISR_STORE_BUCKET"] = c.CacheStoreBucket
	}
	if c.WriterURL != "" && c.WriterSecret != "" {
		env["OCEL_ISR_WRITER_URL"] = c.WriterURL
		env["OCEL_ISR_WRITER_SECRET"] = c.WriterSecret
	}
	if bytecodeCacheEnabled() {
		env["OCEL_BYTECODE_PREFIX"] = c.Prefix
	}
	return env
}

func isrPolicy(c isrConfig) (string, error) {
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"s3:GetObject", "s3:PutObject"},
			"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", c.Bucket, c.Prefix),
		},
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:UpdateItem"},
			"Resource": c.TableARN,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{
					"dynamodb:LeadingKeys": []string{c.tagNamespace() + "*"},
				},
			},
		},
	}

	doc := map[string]any{"Version": "2012-10-17", "Statement": statements}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render isr policy: %w", err)
	}
	return string(out), nil
}

func translateFunction(fn *deploymentsv1.ManifestFunction) functionArgs {
	runtime := defaultFunctionRuntime
	if r := fn.GetRuntime(); r != "" {
		runtime = r
	}
	handler := defaultFunctionEntry
	if h := fn.GetHandler(); h != "" {
		handler = h
	}
	memoryMB := defaultFunctionMemoryMB
	if fn.GetFramework() == frameworkNext {
		memoryMB = nextBundleFunctionMemoryMB
	}
	return functionArgs{
		Runtime:        runtime,
		Handler:        handler,
		MemorySizeMB:   memoryMB,
		TimeoutSeconds: defaultFunctionTimeoutSeconds,
	}
}

func ocelTags(app, env, project string) pulumi.StringMap {
	tags := pulumi.StringMap{tagApp: pulumi.String(app)}
	if env != "" {
		tags[tagEnv] = pulumi.String(env)
	}
	if project != "" {
		tags[tagProject] = pulumi.String(project)
	}
	return tags
}

func functionEnvKey(rt resourcesv1.ResourceType, id string) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", resourceTypeToken(rt), id)
}

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

func postgresEnvPayload(username, password, host string, port int, database string) string {
	conn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", username, password, host, port, database)
	b, _ := json.Marshal(map[string]string{"connectionString": conn})
	return string(b)
}

func bucketEnvPayload(address, bucket string) string {
	b, _ := json.Marshal(map[string]string{"address": address, "bucket": bucket})
	return string(b)
}

func artifactArchivePath(root, artifactPath string) string {
	return filepath.Join(root, artifactPath)
}

func collectFunctionOutput(logicalName, url string) *deploymentsv1.ResourceOutput {
	return &deploymentsv1.ResourceOutput{
		LogicalName: logicalName,
		Output: &deploymentsv1.ResourceOutput_Function{
			Function: &deploymentsv1.FunctionOutput{Url: url},
		},
	}
}

type executionRole struct {
	App        string
	Cache      *isrConfig
	VarsKeyARN string

	VarsTableARN   string
	Slug           string
	VarsClass      string
	VarsReferenced []string
}

func appExecutionRole(cfg Config, app string, caches map[string]*isrConfig, bundle appBundle) executionRole {
	role := executionRole{App: app, Cache: caches[app], VarsKeyARN: cfg.VarsKeyARN}
	if bundle.hasLive() {
		role.VarsTableARN = cfg.VarsTableARN
		role.VarsReferenced = bundle.Referenced
		role.Slug = cfg.Slug
		role.VarsClass = cfg.VarsClass
	}
	return role
}

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
	varsPolicy, err := varsReadPolicy(r)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-vars", &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: pulumi.String(varsPolicy),
	}); err != nil {
		return nil, err
	}
	return role, nil
}

func functionEnv(base map[string]string, args functionArgs, isr *isrConfig) map[string]string {
	env := make(map[string]string, len(base))
	maps.Copy(env, base)
	env["AWS_LAMBDA_EXEC_WRAPPER"] = execWrapper
	env["OCEL_HANDLER"] = "/var/task/" + args.Handler
	if isr != nil {
		maps.Copy(env, isr.env())
	}
	return env
}

func registerFunction(ctx *pulumi.Context, logicalName string, tags pulumi.StringMap, args functionArgs, artifact artifactRef, base map[string]string, isr *isrConfig, roleArn pulumi.StringInput) error {
	env := pulumi.StringMap{}
	for key, value := range functionEnv(base, args, isr) {
		env[key] = pulumi.String(value)
	}

	resourceName := lambdaResourceName(logicalName)

	fn, err := lambda.NewFunction(ctx, resourceName, &lambda.FunctionArgs{
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

		Tags: tags,

		Layers: pulumi.StringArray{
			pulumi.String(membraneLayerARN()),
		},
	})
	if err != nil {
		return err
	}

	url, err := lambda.NewFunctionUrl(ctx, resourceName+"-url", &lambda.FunctionUrlArgs{
		FunctionName:      fn.Name,
		AuthorizationType: pulumi.String(functionURLAuthIAM),
		InvokeMode:        pulumi.String(functionURLInvokeModeStream),
	})
	if err != nil {
		return err
	}

	ctx.Export(logicalName, pulumi.Map{
		outputKeyFunctionURL:  url.FunctionUrl,
		outputKeyFunctionName: fn.Name,
	})
	return nil
}

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

func bucketEnvValue(bucket pulumi.StringInput) pulumi.StringOutput {
	return bucket.ToStringOutput().ApplyT(func(b string) string {
		return bucketEnvPayload(deferredRuntimeAddress, b)
	}).(pulumi.StringOutput)
}

func parseManagedPassword(secretJSON string) (string, error) {
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(secretJSON), &parsed); err != nil {
		return "", fmt.Errorf("parse managed secret: %w", err)
	}
	return parsed.Password, nil
}
