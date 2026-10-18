package function

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

const (
	defaultRuntime = component.LambdaNodeRuntime

	pythonRuntime = "python" + arch.PythonVersion

	providedRuntime = "provided.al2023"

	providedHandler = "bootstrap"

	defaultEntry = "src/server.js"

	defaultMemoryMB       = 1024
	defaultTimeoutSeconds = int(awsports.RequestTimeout / time.Second)

	nextBundleMemoryMB = 1769

	execWrapper = "/opt/" + providedHandler

	MaxBytes = 200 << 20

	tagImageOptimizer = "image-optimizer"

	invokeModeStream = "RESPONSE_STREAM"

	OutputKeyURL = "url"

	OutputKeyName = "functionName"
)

type Args struct {
	Runtime        string
	Handler        string
	Arch           string
	MemorySizeMB   int
	TimeoutSeconds int
	InvokeMode     string
	Tags           map[string]string
	Network        *functionnetwork.Network
}

type Bytecode struct {
	Bucket string
	Prefix string
}

func (c Bytecode) env() map[string]string {
	return map[string]string{
		"OCEL_BYTECODE_BUCKET": c.Bucket,
		"OCEL_BYTECODE_PREFIX": c.Prefix,
	}
}

func bytecodePolicy(c Bytecode) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject", "s3:PutObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", c.Bucket, c.Prefix),
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render bytecode policy: %w", err)
	}
	return string(out), nil
}

func assetReadPolicy(h *DispatchHost) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", h.AssetBucket, h.AssetPrefix),
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render asset read policy: %w", err)
	}
	return string(out), nil
}

func renderDispatchInvokePolicy(siblings []string, account string, optimizer bool) (string, error) {
	statements := []any{}
	if len(siblings) > 0 {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   functionurl.InvokerActions(),
			"Resource": siblings,
		})
	}
	if optimizer {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   functionurl.InvokerActions(),
			"Resource": fmt.Sprintf("arn:aws:lambda:*:%s:function:*", account),
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:ResourceTag/" + component.TagComponent: tagImageOptimizer,
				},
			},
		})
	}
	if len(statements) == 0 {
		return "", nil
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render dispatch invoke policy: %w", err)
	}
	return string(out), nil
}

func Coordinate(project string, stack naming.StackName, logicalName string) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindFunction,
		Name:    logicalLocalName(logicalName),
		Release: stack.Release,
	}
}

func logicalLocalName(logicalName string) string {
	fields := strings.Split(logicalName, naming.FieldSeparator)
	return fields[len(fields)-1]
}

func artifactArchivePath(root, artifactPath string) string {
	return filepath.Join(root, artifactPath)
}

type Artifact struct {
	Bucket string
	Key    string
}

type ExecutionRole struct {
	App       string
	Tags      map[string]string
	Cache     *site.ISR
	Bytecode  *Bytecode
	Variables variables.Reader
	Boundary  string
	Path      string
	VPCAccess bool
	Dispatch  *DispatchHost

	BindingPolicies []iam.BindingPolicy

	TasksPolicy string
}

func NewRole(ctx *pulumi.Context, coord naming.Coordinate, r ExecutionRole) (*awsiam.Role, error) {
	if r.Boundary == "" {
		return nil, fmt.Errorf("mint the execution role for %s: this deploy resolved no app boundary, and a role made without one is capped by nothing", r.App)
	}
	id := naming.ResourceID(naming.KindRole, component.AppRoleLocalName)
	role, err := awsiam.NewRole(ctx, id, &awsiam.RoleArgs{
		NamePrefix:          pulumi.String(component.RolePrefix(coord)),
		Description:         component.Describe(coord, "execution role for this app's functions"),
		AssumeRolePolicy:    pulumi.String(iam.AssumeRolePolicy(iam.LambdaPrincipal)),
		PermissionsBoundary: pulumi.String(r.Boundary),
		Path:                pulumi.String(r.Path),
		Tags:                component.Tags(coord.Kind, "", r.Tags),
	})
	if err != nil {
		return nil, err
	}
	if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "logs"), &awsiam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(iam.LambdaBasicExecutionPolicyARN),
	}); err != nil {
		return nil, err
	}
	if r.VPCAccess {
		if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "vpc"), &awsiam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String(iam.LambdaVPCAccessPolicyARN),
		}); err != nil {
			return nil, err
		}
	}
	if r.Cache != nil {
		policy, err := r.Cache.Policy()
		if err != nil {
			return nil, err
		}
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "isr", "cache"), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.Bytecode != nil {
		policy, err := bytecodePolicy(*r.Bytecode)
		if err != nil {
			return nil, err
		}
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "bytecode", "cache"), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.Dispatch != nil && r.Dispatch.AssetBucket != "" {
		policy, err := assetReadPolicy(r.Dispatch)
		if err != nil {
			return nil, err
		}
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "asset", "read"), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	for _, binding := range r.BindingPolicies {
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "binding", binding.Binding), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(binding.Policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.TasksPolicy != "" {
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "tasks"), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(r.TasksPolicy),
		}); err != nil {
			return nil, err
		}
	}
	variablesPolicy, err := variables.ReadPolicy(r.Variables)
	if err != nil {
		return nil, err
	}
	if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "variables", "read"), &awsiam.RolePolicyArgs{
		Role:   role.Name,
		Policy: pulumi.String(variablesPolicy),
	}); err != nil {
		return nil, err
	}
	return role, nil
}

func (a Args) LambdaHandler() string {
	if a.Runtime == providedRuntime {
		return providedHandler
	}
	return a.Handler
}

func Env(base map[string]string, args Args, isr *site.ISR, bytecode *Bytecode) map[string]string {
	env := make(map[string]string, len(base))
	maps.Copy(env, base)
	if args.Runtime != providedRuntime {
		env["AWS_LAMBDA_EXEC_WRAPPER"] = execWrapper
	}
	env["OCEL_HANDLER"] = "/var/task/" + args.Handler
	if isr != nil {
		maps.Copy(env, isr.Env())
	}
	if bytecode != nil {
		maps.Copy(env, bytecode.env())
	}
	return env
}

func RuntimeLayerInput(arn string) pulumi.StringArrayInput {
	if arn == awsports.RuntimeLayerToPublish {
		return pulumi.StringArray{pulumi.UnsafeUnknownOutput(nil).ApplyT(func(any) string { return "" }).(pulumi.StringOutput)}
	}
	return pulumi.StringArray{pulumi.String(arn)}
}

func registerFunction(ctx *pulumi.Context, logicalName string, coord naming.Coordinate, bundle string, args Args, artifact Artifact, base map[string]string, resolved map[string]pulumi.StringInput, isr *site.ISR, bytecode *Bytecode, roleArn pulumi.StringInput, layers pulumi.StringArrayInput, urlAuth, kmsKeyARN string, opts ...pulumi.ResourceOption) (functionRef, error) {
	var none functionRef

	env := pulumi.StringMap{}
	for key, value := range Env(base, args, isr, bytecode) {
		env[key] = pulumi.String(value)
	}
	for key, value := range resolved {
		env[key] = value
	}

	resourceName := coord.PhysicalName(component.MaxLambdaBaseNameLen)
	if bundle == "" {
		bundle = naming.PathSeparator + coord.Name
	}

	logs, err := component.NewLambdaLogGroup(ctx, naming.ResourceID(naming.KindFunction, coord.Name, "logs"), resourceName, coord.Kind, bundle, args.Tags, opts...)
	if err != nil {
		return none, err
	}

	fn, err := lambda.NewFunction(ctx, resourceName, &lambda.FunctionArgs{
		Description: component.Describe(coord, "bundle "+bundle),
		Runtime:     pulumi.String(args.Runtime),
		Handler:     pulumi.String(args.LambdaHandler()),
		Role:        roleArn,
		S3Bucket:    pulumi.String(artifact.Bucket),
		S3Key:       pulumi.String(artifact.Key),
		MemorySize:  pulumi.Int(args.MemorySizeMB),
		Timeout:     pulumi.Int(args.TimeoutSeconds),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: env,
		},
		KmsKeyArn:     component.OptionalARN(kmsKeyARN),
		LoggingConfig: component.LambdaLogging(logs),

		Tags: component.Tags(coord.Kind, bundle, args.Tags),

		Architectures: pulumi.StringArray{pulumi.String(args.Arch)},

		Layers: layers,

		VpcConfig: args.Network.VPCConfig(),
	}, opts...)
	if err != nil {
		return none, err
	}

	url, err := lambda.NewFunctionUrl(ctx, naming.ResourceID(naming.KindFunction, coord.Name, "url"), &lambda.FunctionUrlArgs{
		FunctionName:      fn.Name,
		AuthorizationType: pulumi.String(urlAuth),
		InvokeMode:        pulumi.String(args.InvokeMode),
	})
	if err != nil {
		return none, err
	}

	if urlAuth == functionurl.AuthNone {
		for _, permission := range functionurl.Public() {
			args := &lambda.PermissionArgs{
				Action:    pulumi.String(permission.Action),
				Function:  fn.Name,
				Principal: pulumi.String("*"),
			}
			if permission.URLAuthType != "" {
				args.FunctionUrlAuthType = pulumi.String(permission.URLAuthType)
			}
			if permission.ViaURL {
				args.InvokedViaFunctionUrl = pulumi.Bool(true)
			}
			if _, err := lambda.NewPermission(ctx, naming.ResourceID(naming.KindFunction, coord.Name, "url", strings.ToLower(permission.Verb())), args); err != nil {
				return none, err
			}
		}
	}

	ctx.Export(logicalName, pulumi.Map{
		OutputKeyURL:  url.FunctionUrl,
		OutputKeyName: fn.Name,
	})
	return functionRef{URL: url.FunctionUrl, ARN: fn.Arn}, nil
}

type functionRef struct {
	URL pulumi.StringOutput
	ARN pulumi.StringOutput
}

func ResourceNames(project string, stack naming.StackName, logicalName string) map[string]component.ResourceRef {
	coord := Coordinate(project, stack, logicalName)
	return map[string]component.ResourceRef{
		"lambda":        {Token: component.TokenLambdaFunction, Name: coord.PhysicalName(component.MaxLambdaBaseNameLen)},
		"url":           {Token: component.TokenLambdaFunctionURL, Name: naming.ResourceID(naming.KindFunction, coord.Name, "url")},
		"urlPermission": {Token: component.TokenLambdaPermission, Name: naming.ResourceID(naming.KindFunction, coord.Name, "url", "invoke")},
		"logGroup":      {Token: component.TokenLogGroup, Name: naming.ResourceID(naming.KindFunction, coord.Name, "logs")},
		"role":          {Token: component.TokenIAMRole, Name: naming.ResourceID(naming.KindRole, component.AppRoleLocalName)},
	}
}

func FindArtifact(buckets site.Buckets, ref provider.ArtifactRef) (Artifact, error) {
	bucket, err := buckets.Find(ref.Bucket)
	if err != nil {
		return Artifact{}, err
	}
	if ref.Key == "" {
		return Artifact{}, fmt.Errorf("no code was uploaded for it")
	}
	return Artifact{Bucket: bucket, Key: ref.Key}, nil
}
