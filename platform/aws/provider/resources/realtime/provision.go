package realtime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appsync"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	AuthorizerPayload                = "realtime-authorizer"
	realtimeAuthorizerRuntime        = "provided.al2023"
	realtimeAuthorizerHandler        = "bootstrap"
	realtimeAuthorizerMemoryMB       = 128
	realtimeAuthorizerTimeoutSeconds = 10

	appSyncLambdaAuth = "AWS_LAMBDA"
	appSyncIAMAuth    = "AWS_IAM"
	appSyncPrincipal  = "appsync.amazonaws.com"

	appSyncHTTPDNSKey     = "HTTP"
	appSyncRealtimeDNSKey = "REALTIME"

	maxAppSyncAPINameLen       = 50
	realtimeWord               = "realtime"
	authorizerSuffix           = "authorizer"
	authorizerInvokePermission = "appsync-invoke"
	authorizerReadsAPIPolicy   = "api"
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
	RolePath    string
}

func apiName(namespace, project, env string) (string, error) {
	at := naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: naming.KindRealtime, Name: realtimeWord}
	return at.TaggedResourceName(namespace).Fit(maxAppSyncAPINameLen)
}

func encodeVerifyKeys(namespaces []realtimeNamespace) (string, error) {
	keys := make(map[string]string, len(namespaces))
	for _, ns := range namespaces {
		keys[ns.Namespace] = base64.StdEncoding.EncodeToString(ns.VerifyKey)
	}
	encoded, err := json.Marshal(keys)
	return string(encoded), err
}

func registerRealtime(ctx *pulumi.Context, stack component.Stack, args realtimeArgs) error {
	at := naming.Coordinate{Project: stack.Project, Env: stack.Env, App: naming.InfraApp, Kind: naming.KindRealtimeAuthorizer}
	if args.BoundaryARN == "" {
		return fmt.Errorf("realtime: this deploy resolved no app boundary, and the authorizer's role made without one is capped by nothing")
	}
	verifyKeys, err := encodeVerifyKeys(args.Namespaces)
	if err != nil {
		return err
	}
	physical, err := apiName(stack.Namespace, stack.Project, stack.Env)
	if err != nil {
		return err
	}
	name := component.NameOf(physical)
	authorizerName := name.Child(authorizerSuffix)

	role, _, err := component.NewAppRole(ctx, component.AppRole{
		Name:        authorizerName,
		Description: pulumi.String(at.Description("execution role for the realtime authorizer")),
		Principal:   iam.LambdaPrincipal,
		Boundary:    args.BoundaryARN,
		Path:        args.RolePath,
		Managed:     []string{iam.LambdaBasicExecutionPolicyARN},
	})
	if err != nil {
		return err
	}

	logs, err := component.NewLambdaLogGroup(ctx, authorizerName, naming.KindRealtimeAuthorizer, "", nil)
	if err != nil {
		return err
	}

	authorizer, err := lambda.NewFunction(ctx, authorizerName.Pulumi, &lambda.FunctionArgs{
		Name:          authorizerName.AWS,
		Runtime:       pulumi.String(realtimeAuthorizerRuntime),
		Handler:       pulumi.String(realtimeAuthorizerHandler),
		Architectures: pulumi.StringArray{pulumi.String(arch.ARM64)},
		MemorySize:    pulumi.Int(realtimeAuthorizerMemoryMB),
		Timeout:       pulumi.Int(realtimeAuthorizerTimeoutSeconds),
		Role:          role.Arn,
		Description:   component.CapDescription(at.Description("verifies the realtime tokens this stage's apps sign"), component.MaxDescriptionLen),
		LoggingConfig: component.LambdaLogging(logs),
		Tags:          component.Tags(naming.KindRealtimeAuthorizer, "", nil),
		S3Bucket:      pulumi.String(args.Authorizer.Bucket),
		S3Key:         pulumi.String(args.Authorizer.Key),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{awsports.RealtimeVerifyKeysEnvVar: pulumi.String(verifyKeys)},
		},
	})
	if err != nil {
		return err
	}

	api, err := appsync.NewApi(ctx, name.Pulumi, &appsync.ApiArgs{
		Name: name.AWS,
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
		Tags: component.Tags(naming.KindRealtime, "", nil),
	})
	if err != nil {
		return err
	}

	if _, err := lambda.NewPermission(ctx, authorizerName.Part(authorizerInvokePermission), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  authorizer.Name,
		Principal: pulumi.String(appSyncPrincipal),
		SourceArn: api.ApiArn,
	}); err != nil {
		return err
	}

	readsOwnAPI := api.ApiArn.ApplyT(func(arn string) (string, error) {
		return iam.InlinePolicy([]string{"appsync:GetApi"}, []string{arn}, nil)
	}).(pulumi.StringOutput)
	if _, err := component.NewRolePolicy(ctx, authorizerName, role.Name, authorizerReadsAPIPolicy, readsOwnAPI); err != nil {
		return err
	}

	httpHost := api.Dns.MapIndex(pulumi.String(appSyncHTTPDNSKey))
	realtimeHost := api.Dns.MapIndex(pulumi.String(appSyncRealtimeDNSKey))
	for _, ns := range args.Namespaces {
		at := component.ResourceCoordinate(stack.Project, stack.Env, ns.LogicalName, naming.KindRealtime)
		if _, err := appsync.NewChannelNamespace(ctx, name.Part(ns.Namespace), &appsync.ChannelNamespaceArgs{
			ApiId: api.ApiId,
			Name:  pulumi.String(ns.Namespace),
			Tags:  component.Tags(naming.KindRealtime, "", map[string]string{component.TagResource: at.Name}),
		}); err != nil {
			return err
		}
		ctx.Export(ns.LogicalName, pulumi.Map{
			component.OutputKeyHost: httpHost,
			OutputKeyRealtimeHost:   realtimeHost,
			OutputKeyAPIARN:         api.ApiArn,
			OutputKeyNamespace:      pulumi.String(ns.Namespace),
			OutputKeySigningKey:     pulumi.String(ns.SigningKeySecret),
		})
	}
	return nil
}
