package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appsync"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	realtimeAuthorizerKeyPrefix      = "ocel-realtime-authorizer"
	realtimeAuthorizerRuntime        = "provided.al2023"
	realtimeAuthorizerHandler        = "bootstrap"
	realtimeAuthorizerMemoryMB       = 128
	realtimeAuthorizerTimeoutSeconds = 10

	appSyncLambdaAuth = "AWS_LAMBDA"
	appSyncIAMAuth    = "AWS_IAM"
	appSyncPrincipal  = "appsync.amazonaws.com"

	appSyncHTTPDNSKey     = "HTTP"
	appSyncRealtimeDNSKey = "REALTIME"

	maxAppSyncAPINameLen = 50
)

type realtimeNamespace struct {
	LogicalName      string
	Namespace        string
	VerifyKey        []byte
	SigningKeySecret string
}

type realtimeArgs struct {
	Namespaces  []realtimeNamespace
	Authorizer  payloads.Placement
	BoundaryARN string
}

func realtimeResourcesOf(resources []provider.Resource) []provider.Resource {
	var out []provider.Resource
	for _, resource := range resources {
		if resource.Type == provider.BindingRealtime && resource.Binding == "" {
			out = append(out, resource)
		}
	}
	return out
}

func appSyncAPIName(project, env string) string {
	scope := awsports.AppScope + naming.WordSeparator
	return scope + naming.Fit(maxAppSyncAPINameLen-len(scope), naming.WordSeparator, naming.Fixed(project), naming.Compressible(env))
}

func encodeVerifyKeys(namespaces []realtimeNamespace) (string, error) {
	keys := make(map[string]string, len(namespaces))
	for _, ns := range namespaces {
		keys[ns.Namespace] = base64.StdEncoding.EncodeToString(ns.VerifyKey)
	}
	encoded, err := json.Marshal(keys)
	return string(encoded), err
}

func registerRealtime(ctx *pulumi.Context, project, env string, args realtimeArgs) error {
	at := naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: naming.KindRealtimeAuthorizer}
	if args.BoundaryARN == "" {
		return fmt.Errorf("realtime: this deploy resolved no app boundary, and the authorizer's role made without one is capped by nothing")
	}
	verifyKeys, err := encodeVerifyKeys(args.Namespaces)
	if err != nil {
		return err
	}
	authorizerID := naming.ResourceID(naming.KindRealtimeAuthorizer, "")

	role, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRealtimeAuthorizer, "", "role"), &iam.RoleArgs{
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy("lambda.amazonaws.com")),
		Description:         pulumi.String(at.Description("execution role for the realtime authorizer")),
		PermissionsBoundary: pulumi.String(args.BoundaryARN),
		Tags:                resourceTags(naming.KindRole, "", nil),
	})
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRealtimeAuthorizer, "", "logs-policy"), &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return err
	}

	logs, err := newLambdaLogGroup(ctx,
		naming.ResourceID(naming.KindRealtimeAuthorizer, "", "logs"),
		at.PhysicalPrefix(maxLambdaBaseNameLen-len(naming.KindRealtimeAuthorizer))+string(naming.KindRealtimeAuthorizer),
		naming.KindRealtimeAuthorizer, "", nil)
	if err != nil {
		return err
	}

	authorizer, err := lambda.NewFunction(ctx, authorizerID, &lambda.FunctionArgs{
		Runtime:       pulumi.String(realtimeAuthorizerRuntime),
		Handler:       pulumi.String(realtimeAuthorizerHandler),
		Architectures: pulumi.StringArray{pulumi.String(arch.ARM64)},
		MemorySize:    pulumi.Int(realtimeAuthorizerMemoryMB),
		Timeout:       pulumi.Int(realtimeAuthorizerTimeoutSeconds),
		Role:          role.Arn,
		Description:   capDescription(at.Description("verifies the realtime tokens this stage's apps sign"), maxDescriptionLen),
		LoggingConfig: lambdaLogging(logs),
		Tags:          resourceTags(naming.KindRealtimeAuthorizer, "", nil),
		S3Bucket:      pulumi.String(args.Authorizer.Bucket),
		S3Key:         pulumi.String(args.Authorizer.Key),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{awsports.RealtimeVerifyKeysEnvVar: pulumi.String(verifyKeys)},
		},
	})
	if err != nil {
		return err
	}

	api, err := appsync.NewApi(ctx, naming.ResourceID(naming.KindRealtime, ""), &appsync.ApiArgs{
		Name: pulumi.String(appSyncAPIName(project, env)),
		EventConfig: &appsync.ApiEventConfigArgs{
			AuthProviders: appsync.ApiEventConfigAuthProviderArray{
				&appsync.ApiEventConfigAuthProviderArgs{
					AuthType: pulumi.String(appSyncLambdaAuth),
					LambdaAuthorizerConfig: &appsync.ApiEventConfigAuthProviderLambdaAuthorizerConfigArgs{
						AuthorizerUri:                authorizer.Arn,
						AuthorizerResultTtlInSeconds: pulumi.Int(0),
					},
				},
				&appsync.ApiEventConfigAuthProviderArgs{AuthType: pulumi.String(appSyncIAMAuth)},
			},
			ConnectionAuthModes: appsync.ApiEventConfigConnectionAuthModeArray{
				&appsync.ApiEventConfigConnectionAuthModeArgs{AuthType: pulumi.String(appSyncLambdaAuth)},
				&appsync.ApiEventConfigConnectionAuthModeArgs{AuthType: pulumi.String(appSyncIAMAuth)},
			},
			DefaultSubscribeAuthModes: appsync.ApiEventConfigDefaultSubscribeAuthModeArray{
				&appsync.ApiEventConfigDefaultSubscribeAuthModeArgs{AuthType: pulumi.String(appSyncLambdaAuth)},
				&appsync.ApiEventConfigDefaultSubscribeAuthModeArgs{AuthType: pulumi.String(appSyncIAMAuth)},
			},
			DefaultPublishAuthModes: appsync.ApiEventConfigDefaultPublishAuthModeArray{
				&appsync.ApiEventConfigDefaultPublishAuthModeArgs{AuthType: pulumi.String(appSyncIAMAuth)},
			},
		},
		Tags: resourceTags(naming.KindRealtime, "", nil),
	})
	if err != nil {
		return err
	}

	if _, err := lambda.NewPermission(ctx, naming.ResourceID(naming.KindRealtimeAuthorizer, "", "permission"), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  authorizer.Name,
		Principal: pulumi.String(appSyncPrincipal),
		SourceArn: api.ApiArn,
	}); err != nil {
		return err
	}

	readsOwnAPI := api.ApiArn.ApplyT(func(arn string) (string, error) {
		return inlinePolicy([]string{"appsync:GetApi"}, []string{arn}, nil)
	}).(pulumi.StringOutput)
	if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRealtimeAuthorizer, "", "role", "policy", "api"), &iam.RolePolicyArgs{
		Role:   role.ID(),
		Policy: readsOwnAPI,
	}); err != nil {
		return err
	}

	httpHost := api.Dns.MapIndex(pulumi.String(appSyncHTTPDNSKey))
	realtimeHost := api.Dns.MapIndex(pulumi.String(appSyncRealtimeDNSKey))
	for _, ns := range args.Namespaces {
		at := resourceCoordinate(project, env, ns.LogicalName, naming.KindRealtime)
		if _, err := appsync.NewChannelNamespace(ctx, naming.ResourceID(naming.KindRealtime, at.Name), &appsync.ChannelNamespaceArgs{
			ApiId: api.ApiId,
			Name:  pulumi.String(ns.Namespace),
			Tags:  resourceTags(naming.KindRealtime, "", map[string]string{tagResource: at.Name}),
		}); err != nil {
			return err
		}
		ctx.Export(ns.LogicalName, pulumi.Map{
			outputKeyHost:         httpHost,
			outputKeyRealtimeHost: realtimeHost,
			outputKeyAPIARN:       api.ApiArn,
			outputKeyNamespace:    pulumi.String(ns.Namespace),
			outputKeySigningKey:   pulumi.String(ns.SigningKeySecret),
		})
	}
	return nil
}
