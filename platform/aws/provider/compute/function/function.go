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
	"github.com/ocelhq/ocel/pkg/processenv"
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

	isrCachePolicy      = "isr-cache"
	bytecodeCachePolicy = "bytecode-cache"
	assetReadPolicyName = "asset-read"
	dispatchPolicyName  = "dispatch-invoke"

	urlPart = "url"
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
	Bucket            string
	StorageCoordinate naming.Coordinate
}

func (c Bytecode) env() map[string]string {
	env := processenv.StorageEnv(c.StorageCoordinate, naming.Buckets{naming.StorageArtifacts: c.Bucket})
	env[processenv.CompileCacheEnvVar] = "1"
	return env
}

func bytecodePolicy(c Bytecode) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject", "s3:PutObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s*", c.Bucket, c.StorageCoordinate.StoragePrefix(naming.ObjectBytecode)),
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
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s*", h.EdgeBucket, h.StorageCoordinate.StoragePrefix(naming.ObjectAssets)),
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
	Namespace string
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

	Policies map[string]string
}

func NewRole(ctx *pulumi.Context, coord naming.Coordinate, r ExecutionRole) (*awsiam.Role, []pulumi.Resource, error) {
	name, err := component.RoleName(r.Namespace, coord)
	if err != nil {
		return nil, nil, err
	}
	managed := []string{iam.LambdaBasicExecutionPolicyARN}
	if r.VPCAccess {
		managed = append(managed, iam.LambdaVPCAccessPolicyARN)
	}
	documents := map[string]pulumi.StringInput{}
	if r.Cache != nil {
		document, err := r.Cache.Policy()
		if err != nil {
			return nil, nil, err
		}
		documents[isrCachePolicy] = pulumi.String(document)
	}
	if r.Bytecode != nil {
		document, err := bytecodePolicy(*r.Bytecode)
		if err != nil {
			return nil, nil, err
		}
		documents[bytecodeCachePolicy] = pulumi.String(document)
	}
	if r.Dispatch != nil && r.Dispatch.EdgeBucket != "" {
		document, err := assetReadPolicy(r.Dispatch)
		if err != nil {
			return nil, nil, err
		}
		documents[assetReadPolicyName] = pulumi.String(document)
	}
	for _, binding := range r.BindingPolicies {
		documents[component.BindingPolicyName(binding.Binding)] = pulumi.String(binding.Policy)
	}
	if r.TasksPolicy != "" {
		documents[component.TasksPolicyName] = pulumi.String(r.TasksPolicy)
	}
	for policy, document := range r.Policies {
		documents[policy] = pulumi.String(document)
	}
	variablesPolicy, err := variables.ReadPolicy(r.Variables)
	if err != nil {
		return nil, nil, err
	}
	if variablesPolicy != "" {
		documents[component.VariablesPolicyName] = pulumi.String(variablesPolicy)
	}
	return component.NewAppRole(ctx, component.AppRole{
		Name:        name,
		Description: component.Describe(coord, "execution role for this app's functions"),
		Principal:   iam.LambdaPrincipal,
		Boundary:    r.Boundary,
		Path:        r.Path,
		Tags:        r.Tags,
		Managed:     managed,
		Policies:    documents,
	})
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

func registerFunction(ctx *pulumi.Context, logicalName string, coord naming.Coordinate, name component.Name, bundle string, args Args, artifact Artifact, base map[string]string, resolved map[string]pulumi.StringInput, isr *site.ISR, bytecode *Bytecode, roleArn pulumi.StringInput, layers pulumi.StringArrayInput, urlAuth, kmsKeyARN string, opts ...pulumi.ResourceOption) (functionRef, error) {
	var none functionRef

	env := pulumi.StringMap{}
	for key, value := range Env(base, args, isr, bytecode) {
		env[key] = pulumi.String(value)
	}
	for key, value := range resolved {
		env[key] = value
	}

	if bundle == "" {
		bundle = naming.PathSeparator + coord.Name
	}

	logs, err := component.NewLambdaLogGroup(ctx, name, coord.Kind, bundle, args.Tags, opts...)
	if err != nil {
		return none, err
	}

	fn, err := lambda.NewFunction(ctx, name.Pulumi, &lambda.FunctionArgs{
		Name:        name.AWS,
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

	url, err := lambda.NewFunctionUrl(ctx, name.Part(urlPart), &lambda.FunctionUrlArgs{
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
			if _, err := lambda.NewPermission(ctx, urlPermissionName(name, permission), args); err != nil {
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

type Names struct {
	Function component.Name
	Role     component.Name
}

func NamesFor(namespace, project string, stack naming.StackName, logicalName string) (Names, error) {
	function, err := component.PhysicalName(namespace, Coordinate(project, stack, logicalName), component.MaxLambdaNameLen)
	if err != nil {
		return Names{}, err
	}
	role, err := component.RoleName(namespace, component.AppRoleCoordinate(project, stack))
	if err != nil {
		return Names{}, err
	}
	return Names{Function: component.NameOf(function), Role: role}, nil
}

func (n Names) Surfaces() map[string][]component.ResourceRef {
	var urlPermissions []component.ResourceRef
	for _, permission := range functionurl.Public() {
		urlPermissions = append(urlPermissions, component.ResourceRef{Token: component.TokenLambdaPermission, Name: urlPermissionName(n.Function, permission)})
	}
	return map[string][]component.ResourceRef{
		"lambda":        {n.Lambda()},
		"url":           {n.URL()},
		"urlPermission": urlPermissions,
		"logGroup":      {n.LogGroup()},
		"role":          {{Token: component.TokenIAMRole, Name: n.Role.Pulumi}},
	}
}

func (n Names) Lambda() component.ResourceRef {
	return component.ResourceRef{Token: component.TokenLambdaFunction, Name: n.Function.Pulumi}
}

func (n Names) URL() component.ResourceRef {
	return component.ResourceRef{Token: component.TokenLambdaFunctionURL, Name: n.Function.Part(urlPart)}
}

func (n Names) LogGroup() component.ResourceRef {
	return component.ResourceRef{Token: component.TokenLogGroup, Name: component.LambdaLogGroupName(n.Function).Pulumi}
}

func urlPermissionName(function component.Name, permission functionurl.Permission) string {
	return function.Part(strings.ToLower(permission.Verb()))
}

func FindArtifact(buckets site.Buckets, ref provider.ArtifactRef) (Artifact, error) {
	if ref.Key == "" {
		return Artifact{}, fmt.Errorf("no code was uploaded for it")
	}
	bucket, err := buckets.Find(ref.Key)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Bucket: bucket, Key: ref.Key}, nil
}
