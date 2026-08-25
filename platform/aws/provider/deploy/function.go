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
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	defaultFunctionRuntime = "nodejs24.x"

	defaultFunctionEntry = "src/server.js"

	defaultFunctionMemoryMB       = 1024
	defaultFunctionTimeoutSeconds = 30

	nextBundleFunctionMemoryMB = 1769

	lambdaConfigHandler = "index.handler"

	execWrapper = "/opt/ocel/bootstrap"

	membraneLayerLocalName = "membrane"

	membraneLayerRuntime      = "nodejs24.x"
	membraneLayerArchitecture = "x86_64"

	maxLayerNameLen = 64

	bytecodeCacheEnv = "OCEL_BYTECODE_CACHE"

	bytecodeEmbedEnv = "OCEL_BYTECODE_EMBED"

	functionURLAuthIAM  = "AWS_IAM"
	functionURLAuthNone = "NONE"

	tagComponent = "ocel:component"
	tagRoute     = "ocel:route"

	tagImageOptimizer = "image-optimizer"

	roleLocalName = "app"

	lambdaServicePrincipal = "lambda.amazonaws.com"

	maxDescriptionLen = 256

	maxRoleNameLen       = 64
	iamAutonameSuffixLen = 26
	maxRolePrefixLen     = maxRoleNameLen - iamAutonameSuffixLen

	maxLambdaNameLen        = 64
	lambdaAutonameSuffixLen = 8
	maxLambdaBaseNameLen    = maxLambdaNameLen - lambdaAutonameSuffixLen

	functionURLInvokeModeStream = "RESPONSE_STREAM"

	outputKeyFunctionURL = "url"

	outputKeyFunctionName = "functionName"

	lambdaSyncInvokePayloadLimitBytes = 6291456

	lambdaEventEnvelopeMarginBytes = 2048

	lambdaOriginBodyLimitBytes = lambdaSyncInvokePayloadLimitBytes - lambdaEventEnvelopeMarginBytes
)

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
	InvokeMode     string
	VPC            functionVPC
	Tags           map[string]string
}

type functionVPC struct {
	SubnetIDs        []string
	SecurityGroupIDs []string
}

func (v functionVPC) placed() bool {
	return len(v.SubnetIDs) > 0 || len(v.SecurityGroupIDs) > 0
}

type isrConfig struct {
	Coord     naming.Coordinate
	Namespace string

	Bucket   string
	Prefix   string
	Table    string
	TableARN string

	CacheStoreBucket string

	WriterURL    string
	WriterSecret string
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
	if c.CacheStoreBucket != "" {
		env["OCEL_ISR_STORE_BUCKET"] = c.CacheStoreBucket
	}
	if c.WriterURL != "" && c.WriterSecret != "" {
		env["OCEL_ISR_WRITER_URL"] = c.WriterURL
		env["OCEL_ISR_WRITER_SECRET"] = c.WriterSecret
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

func assetReadPolicy(h *routerHost) (string, error) {
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

func routerInvokePolicy(siblings []string, account string, optimizer bool) (string, error) {
	statements := []any{}
	if len(siblings) > 0 {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"lambda:InvokeFunctionUrl"},
			"Resource": siblings,
		})
	}
	if optimizer {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"lambda:InvokeFunctionUrl"},
			"Resource": fmt.Sprintf("arn:aws:lambda:*:%s:function:*", account),
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:ResourceTag/" + tagComponent: tagImageOptimizer,
				},
			},
		})
	}
	if len(statements) == 0 {
		return "", nil
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render router invoke policy: %w", err)
	}
	return string(out), nil
}

func accountOfARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

func isrPolicy(c isrConfig) (string, error) {
	namespace := c.tagNamespace()
	if namespace == "" {
		return "", fmt.Errorf("isr cache under %q carries no coordinate, so its tag items cannot be scoped to this app", c.Prefix)
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

func membraneLayerCoordinate(project string, stack naming.StackName) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindLayer,
		Name:    membraneLayerLocalName,
		Release: stack.Release,
	}
}

func newMembraneLayer(ctx *pulumi.Context, coord naming.Coordinate, code payloads.Placement) (*lambda.LayerVersion, error) {
	return lambda.NewLayerVersion(ctx, naming.ResourceID(naming.KindLayer, membraneLayerLocalName), &lambda.LayerVersionArgs{
		LayerName:      pulumi.String(coord.PhysicalName(maxLayerNameLen)),
		Description:    describe(coord, "membrane the app's functions boot through"),
		S3Bucket:       pulumi.String(code.Bucket),
		S3Key:          pulumi.String(code.Key),
		SourceCodeHash: pulumi.String(code.SHA256),
		CompatibleRuntimes: pulumi.StringArray{
			pulumi.String(membraneLayerRuntime),
		},
		CompatibleArchitectures: pulumi.StringArray{
			pulumi.String(membraneLayerArchitecture),
		},
	})
}

func logicalLocalName(logicalName string) string {
	fields := strings.Split(logicalName, naming.FieldSeparator)
	return fields[len(fields)-1]
}

func resourceTags(kind naming.Kind, route string, extra map[string]string) pulumi.StringMap {
	tags := pulumi.StringMap{}
	for key, value := range extra {
		tags[key] = pulumi.String(value)
	}
	tags[tagComponent] = pulumi.String(kind.Component())
	if route != "" {
		tags[tagRoute] = pulumi.String(route)
	}
	return tags
}

func describe(c naming.Coordinate, detail string) pulumi.String {
	return capDescription(c.Description(detail+" - release "+c.Release.String()), maxDescriptionLen)
}

func capDescription(described string, limit int) pulumi.String {
	if len(described) > limit {
		described = strings.ToValidUTF8(described[:limit], "")
	}
	return pulumi.String(described)
}

func functionEnvKey(t linksv1.LinkType, id string) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", naming.EnvFragment(t), id)
}

func artifactArchivePath(root, artifactPath string) string {
	return filepath.Join(root, artifactPath)
}

func collectFunctionOutput(logicalName, url string) *progressv1.FunctionOutput {
	return &progressv1.FunctionOutput{LogicalName: logicalName, Url: url}
}

type executionRole struct {
	App        string
	Tags       map[string]string
	Cache      *isrConfig
	Bytecode   *bytecodeConfig
	VarsKeyARN string
	Boundary   string
	VPCAccess  bool
	Router     *routerHost

	ValuesTableARN string
	Slug           string
	VarsClass      string
	VarsReferenced []string

	LinkPolicies []linkPolicy
}

func appExecutionRole(cfg Config, app string, caches map[string]*isrConfig, bytecode map[string]*bytecodeConfig, bundle appBundle, tags map[string]string, policies []linkPolicy, vpcAccess bool, router *routerHost) executionRole {
	role := executionRole{App: app, Cache: caches[app], Bytecode: bytecode[app], VarsKeyARN: cfg.VarsKeyARN, Boundary: cfg.AppBoundaryARN, Tags: tags, LinkPolicies: policies, VPCAccess: vpcAccess, Router: router}
	if bundle.hasLive() {
		role.ValuesTableARN = cfg.StateTableARN
		role.VarsReferenced = bundle.Referenced
		role.Slug = cfg.Slug
		role.VarsClass = string(cfg.Class)
	}
	return role
}

func rolePrefix(coord naming.Coordinate) string {
	return coord.PhysicalName(maxRolePrefixLen-len(naming.WordSeparator)) + naming.WordSeparator
}

func newFunctionRole(ctx *pulumi.Context, coord naming.Coordinate, r executionRole) (*iam.Role, error) {
	id := naming.ResourceID(naming.KindRole, roleLocalName)
	role, err := iam.NewRole(ctx, id, &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(coord)),
		Description:         describe(coord, "execution role for this app's functions"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(lambdaServicePrincipal)),
		PermissionsBoundary: permissionsBoundary(r.Boundary),
		Tags:                resourceTags(coord.Kind, "", r.Tags),
	})
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "logs"), &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return nil, err
	}
	if r.VPCAccess {
		if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "vpc"), &iam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"),
		}); err != nil {
			return nil, err
		}
	}
	if r.Cache != nil {
		policy, err := isrPolicy(*r.Cache)
		if err != nil {
			return nil, err
		}
		if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "isr", "cache"), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.Bytecode != nil && bytecodeCacheEnabled() {
		policy, err := bytecodePolicy(*r.Bytecode)
		if err != nil {
			return nil, err
		}
		if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "bytecode", "cache"), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	if r.Router != nil && r.Router.AssetBucket != "" {
		policy, err := assetReadPolicy(r.Router)
		if err != nil {
			return nil, err
		}
		if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "asset", "read"), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(policy),
		}); err != nil {
			return nil, err
		}
	}
	for _, link := range r.LinkPolicies {
		if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "link", link.Link), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(link.Policy),
		}); err != nil {
			return nil, err
		}
	}
	varsPolicy, err := varsReadPolicy(r)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "vars", "read"), &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: pulumi.String(varsPolicy),
	}); err != nil {
		return nil, err
	}
	return role, nil
}

func functionEnv(base map[string]string, args functionArgs, isr *isrConfig, bytecode *bytecodeConfig) map[string]string {
	env := make(map[string]string, len(base))
	maps.Copy(env, base)
	env["AWS_LAMBDA_EXEC_WRAPPER"] = execWrapper
	env["OCEL_HANDLER"] = "/var/task/" + args.Handler
	if isr != nil {
		maps.Copy(env, isr.env())
	}
	if bytecode != nil && bytecodeCacheEnabled() {
		maps.Copy(env, bytecode.env())
	}
	return env
}

func functionVPCConfig(logicalName string, v functionVPC) (lambda.FunctionVpcConfigPtrInput, error) {
	if !v.placed() {
		return nil, nil
	}
	if len(v.SubnetIDs) == 0 {
		return nil, fmt.Errorf("a transform places %s in a VPC with %d security groups and no subnets; a Lambda reaches a VPC through the subnets it is given, so name at least one", logicalName, len(v.SecurityGroupIDs))
	}
	if len(v.SecurityGroupIDs) == 0 {
		return nil, fmt.Errorf("a transform places %s in %d subnets with no security group; a Lambda in a VPC is refused without one, so name at least one", logicalName, len(v.SubnetIDs))
	}
	return lambda.FunctionVpcConfigArgs{
		SubnetIds:        pulumi.ToStringArray(v.SubnetIDs),
		SecurityGroupIds: pulumi.ToStringArray(v.SecurityGroupIDs),
	}, nil
}

func registerFunction(ctx *pulumi.Context, logicalName string, coord naming.Coordinate, route string, args functionArgs, artifact artifactRef, base map[string]string, resolved map[string]pulumi.StringInput, isr *isrConfig, bytecode *bytecodeConfig, roleArn, layerARN pulumi.StringInput, urlAuth string) (functionRef, error) {
	var none functionRef

	env := pulumi.StringMap{}
	for key, value := range functionEnv(base, args, isr, bytecode) {
		env[key] = pulumi.String(value)
	}
	for key, value := range resolved {
		env[key] = value
	}

	resourceName := coord.PhysicalName(maxLambdaBaseNameLen)
	if route == "" {
		route = naming.PathSeparator + coord.Name
	}

	vpcConfig, err := functionVPCConfig(logicalName, args.VPC)
	if err != nil {
		return none, err
	}

	fn, err := lambda.NewFunction(ctx, resourceName, &lambda.FunctionArgs{
		Description: describe(coord, "route "+route),
		Runtime:     pulumi.String(args.Runtime),
		Handler:     pulumi.String(lambdaConfigHandler),
		Role:        roleArn,
		S3Bucket:    pulumi.String(artifact.Bucket),
		S3Key:       pulumi.String(artifact.Key),
		MemorySize:  pulumi.Int(args.MemorySizeMB),
		Timeout:     pulumi.Int(args.TimeoutSeconds),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: env,
		},
		VpcConfig: vpcConfig,

		Tags: resourceTags(coord.Kind, route, args.Tags),

		Layers: pulumi.StringArray{layerARN},
	})
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

	if urlAuth == functionURLAuthNone {
		if _, err := lambda.NewPermission(ctx, naming.ResourceID(naming.KindFunction, coord.Name, "url", "invoke"), &lambda.PermissionArgs{
			Action:              pulumi.String("lambda:InvokeFunctionUrl"),
			Function:            fn.Name,
			Principal:           pulumi.String("*"),
			FunctionUrlAuthType: pulumi.String(functionURLAuthNone),
		}); err != nil {
			return none, err
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
