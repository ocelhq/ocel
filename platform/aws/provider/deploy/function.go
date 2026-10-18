package deploy

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
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

const (
	defaultFunctionRuntime = "nodejs24.x"

	pythonFunctionRuntime = "python" + arch.PythonVersion

	providedFunctionRuntime = "provided.al2023"

	providedHandler = "bootstrap"

	defaultFunctionEntry = "src/server.js"

	defaultFunctionMemoryMB       = 1024
	defaultFunctionTimeoutSeconds = int(awsports.RequestTimeout / time.Second)

	nextBundleFunctionMemoryMB = 1769

	execWrapper = "/opt/" + providedHandler

	MaxFunctionBytes = 200 << 20

	tagImageOptimizer = "image-optimizer"

	roleLocalName = "app"

	functionURLInvokeModeStream = "RESPONSE_STREAM"

	outputKeyFunctionURL = "url"

	outputKeyFunctionName = "functionName"
)

type functionArgs struct {
	Runtime        string
	Handler        string
	Arch           string
	MemorySizeMB   int
	TimeoutSeconds int
	InvokeMode     string
	Tags           map[string]string
	Network        *functionNetwork
}

type isrConfig struct {
	Coord     naming.Coordinate
	Namespace string

	Bucket   string
	Prefix   string
	Table    string
	TableARN string

	PurgeSecret string

	EdgeReadable bool
}

func (c isrConfig) tagNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	if c.Coord.Project == "" || c.Coord.Env == "" || c.Coord.App == "" || c.Coord.Release.IsZero() {
		return ""
	}
	return naming.ISRTagPrefix(c.Coord.Project, c.Coord.Stack())
}

func (c isrConfig) env() map[string]string {
	env := map[string]string{
		"OCEL_ISR_BUCKET":        c.Bucket,
		"OCEL_ISR_PREFIX":        c.Prefix,
		"OCEL_STATE_TABLE":       c.Table,
		"OCEL_ISR_TAG_NAMESPACE": c.tagNamespace(),
	}
	if c.EdgeReadable {
		env["OCEL_ISR_EDGE_READABLE"] = "1"
	}
	return env
}

type bytecodeConfig struct {
	Bucket string
	Prefix string
}

func (c bytecodeConfig) env() map[string]string {
	return map[string]string{
		"OCEL_BYTECODE_BUCKET": c.Bucket,
		"OCEL_BYTECODE_PREFIX": c.Prefix,
	}
}

func bytecodePolicy(c bytecodeConfig) (string, error) {
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

func assetReadPolicy(h *dispatchHost) (string, error) {
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

func isrPolicy(c isrConfig) (string, error) {
	namespace := c.tagNamespace()
	if namespace == "" {
		return "", fmt.Errorf("isr cache under %q has no coordinate, so its tag items cannot be scoped to this app", c.Prefix)
	}
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
					"dynamodb:LeadingKeys": []string{namespace + "*"},
				},
			},
		},
	}

	if c.EdgeReadable {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"s3:PutObjectTagging"},
			"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/cache/*", c.Bucket, c.Prefix),
		})
	}

	doc := map[string]any{"Version": "2012-10-17", "Statement": statements}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render isr policy: %w", err)
	}
	return string(out), nil
}

func functionCoordinate(project string, stack naming.StackName, logicalName string) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindFunction,
		Name:    logicalLocalName(logicalName),
		Release: stack.Release,
	}
}

func roleCoordinate(project string, stack naming.StackName) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindRole,
		Name:    naming.Join(naming.WordSeparator, roleLocalName, string(naming.KindRole)),
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

type executionRole struct {
	App       string
	Tags      map[string]string
	Cache     *isrConfig
	Bytecode  *bytecodeConfig
	Variables variables.Reader
	Boundary  string
	Path      string
	VPCAccess bool
	Dispatch  *dispatchHost

	BindingPolicies []bindingPolicy

	TasksPolicy string
}

func appExecutionRole(cfg Config, app string, caches map[string]*isrConfig, bytecode map[string]*bytecodeConfig, bundle variables.Bundle, tags map[string]string, policies []bindingPolicy, vpcAccess bool, dispatch *dispatchHost) executionRole {
	role := executionRole{App: app, Cache: caches[app], Bytecode: bytecode[app], Variables: variables.Reader{KeyARN: cfg.VariablesKeyARN}, Boundary: cfg.AppBoundaryARN, Path: cfg.AppRolePath, Tags: tags, BindingPolicies: policies, VPCAccess: vpcAccess, Dispatch: dispatch}
	if bundle.HasLive() {
		role.Variables.TableARN = cfg.VariablesTableARN
		role.Variables.Referenced = bundle.Referenced
		role.Variables.Slug = cfg.Slug
		role.Variables.Tier = string(cfg.Tier)
	}
	return role
}

func newFunctionRole(ctx *pulumi.Context, coord naming.Coordinate, r executionRole) (*awsiam.Role, error) {
	if r.Boundary == "" {
		return nil, fmt.Errorf("mint the execution role for %s: this deploy resolved no app boundary, and a role made without one is capped by nothing", r.App)
	}
	id := naming.ResourceID(naming.KindRole, roleLocalName)
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
	if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "logs"), &awsiam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(iam.LambdaBasicExecutionPolicyARN),
	}); err != nil {
		return nil, err
	}
	if r.VPCAccess {
		if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "vpc"), &awsiam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String(iam.LambdaVPCAccessPolicyARN),
		}); err != nil {
			return nil, err
		}
	}
	if r.Cache != nil {
		policy, err := isrPolicy(*r.Cache)
		if err != nil {
			return nil, err
		}
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "isr", "cache"), &awsiam.RolePolicyArgs{
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
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "bytecode", "cache"), &awsiam.RolePolicyArgs{
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
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "asset", "read"), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	for _, binding := range r.BindingPolicies {
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "binding", binding.Binding), &awsiam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(binding.Policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.TasksPolicy != "" {
		if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "tasks"), &awsiam.RolePolicyArgs{
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
	if _, err := awsiam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "variables", "read"), &awsiam.RolePolicyArgs{
		Role:   role.Name,
		Policy: pulumi.String(variablesPolicy),
	}); err != nil {
		return nil, err
	}
	return role, nil
}

func lambdaHandler(args functionArgs) string {
	if args.Runtime == providedFunctionRuntime {
		return providedHandler
	}
	return args.Handler
}

func functionEnv(base map[string]string, args functionArgs, isr *isrConfig, bytecode *bytecodeConfig) map[string]string {
	env := make(map[string]string, len(base))
	maps.Copy(env, base)
	if args.Runtime != providedFunctionRuntime {
		env["AWS_LAMBDA_EXEC_WRAPPER"] = execWrapper
	}
	env["OCEL_HANDLER"] = "/var/task/" + args.Handler
	if isr != nil {
		maps.Copy(env, isr.env())
	}
	if bytecode != nil {
		maps.Copy(env, bytecode.env())
	}
	return env
}

func runtimeLayerInput(arn string) pulumi.StringArrayInput {
	if arn == awsports.RuntimeLayerToPublish {
		return pulumi.StringArray{pulumi.UnsafeUnknownOutput(nil).ApplyT(func(any) string { return "" }).(pulumi.StringOutput)}
	}
	return pulumi.StringArray{pulumi.String(arn)}
}

func registerFunction(ctx *pulumi.Context, logicalName string, coord naming.Coordinate, bundle string, args functionArgs, artifact artifactRef, base map[string]string, resolved map[string]pulumi.StringInput, isr *isrConfig, bytecode *bytecodeConfig, roleArn pulumi.StringInput, layers pulumi.StringArrayInput, urlAuth, kmsKeyARN string, opts ...pulumi.ResourceOption) (functionRef, error) {
	var none functionRef

	env := pulumi.StringMap{}
	for key, value := range functionEnv(base, args, isr, bytecode) {
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
		Handler:     pulumi.String(lambdaHandler(args)),
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

		VpcConfig: args.Network.vpcConfig(),
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
		outputKeyFunctionURL:  url.FunctionUrl,
		outputKeyFunctionName: fn.Name,
	})
	return functionRef{URL: url.FunctionUrl, ARN: fn.Arn}, nil
}

type functionRef struct {
	URL pulumi.StringOutput
	ARN pulumi.StringOutput
}
