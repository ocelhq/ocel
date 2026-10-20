package bucket

import (
	"fmt"
	"strings"

	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	bucketCORSMaxAgeSeconds = 3600

	maxS3BucketNameLen = 63

	abacEnabled = "Enabled"

	abacPart                  = "abac"
	publicAccessBlockPart     = "public-access-block"
	corsPart                  = "cors"
	notificationPart          = "notification"
	completerInvokePermission = "s3-invoke"
	completerS3Policy         = "s3"
	completerSessionsPolicy   = "sessions"

	bucketNotificationEvent = "s3:ObjectCreated:*"

	uploadCompleterSuffix         = "completer"
	uploadCompleterRuntime        = "provided.al2023"
	uploadCompleterHandler        = "bootstrap"
	uploadCompleterTimeoutSeconds = 30

	EnvStateTable     = "OCEL_RUNTIME_STATE_TABLE"
	EnvSessionPrefix  = "OCEL_RUNTIME_SESSION_PREFIX"
	envAllowedOrigins = "OCEL_UPLOAD_COMPLETER_ALLOWED_ORIGINS"
)

type Sessions struct {
	TableARN  string
	KeyPrefix string
}

func NewSessions(project, env, tableARN string) Sessions {
	return Sessions{TableARN: tableARN, KeyPrefix: naming.SessionKeyPrefix(project, env)}
}

type bucketArgs struct {
	AllowedOrigins []string

	ForceDestroy bool

	Tags map[string]string

	CORS corsRule

	PatchedCORS bool

	NotificationEvents []string

	UploadCompleterRuntime        string
	UploadCompleterHandler        string
	UploadCompleterTimeoutSeconds int

	UploadCompleterS3Actions      []string
	UploadCompleterSessionActions []string
}

type corsRule struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

func translate(spec *provider.BucketSpec) bucketArgs {
	var origins []string
	if spec != nil {
		origins = spec.AllowedOrigins
	}
	return bucketArgs{
		AllowedOrigins: origins,
		CORS: corsRule{
			AllowedOrigins: origins,
			AllowedMethods: []string{"PUT"},
			AllowedHeaders: []string{"*"},
			ExposeHeaders:  []string{"ETag"},
			MaxAgeSeconds:  bucketCORSMaxAgeSeconds,
		},
		NotificationEvents:            []string{bucketNotificationEvent},
		UploadCompleterRuntime:        uploadCompleterRuntime,
		UploadCompleterHandler:        uploadCompleterHandler,
		UploadCompleterTimeoutSeconds: uploadCompleterTimeoutSeconds,
		UploadCompleterS3Actions:      []string{"s3:GetObjectTagging"},
		UploadCompleterSessionActions: []string{"dynamodb:GetItem", "dynamodb:UpdateItem"},
	}
}

func Name(ctx *pulumi.Context, at component.Stack, logicalName string) (component.Name, error) {
	return component.RandomTaggedName(ctx, at.Namespace, component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindBucket), component.MaxLambdaNameLen, uploadCompleterSuffix)
}

func register(ctx *pulumi.Context, stack component.Stack, logicalName string, args bucketArgs, sessions Sessions, completerCode payloads.Placement) error {
	at := component.ResourceCoordinate(stack.Project, stack.Env, logicalName, naming.KindBucket)
	if stack.AppBoundaryARN == "" {
		return fmt.Errorf("bucket %s: this deploy resolved no app boundary, and its upload completer's role made without one is capped by nothing", at.Name)
	}
	name, err := Name(ctx, stack, logicalName)
	if err != nil {
		return err
	}
	tags := component.Tags(at.Kind, "", args.Tags)
	tags[naming.ManagedByTagKey] = pulumi.String(stack.Namespace)
	tags[naming.EnvTierTagKey] = pulumi.String(string(stack.Tier()))

	bucket, err := s3.NewBucketV2(ctx, name.Pulumi, &s3.BucketV2Args{
		Bucket:       name.AWS,
		ForceDestroy: pulumi.Bool(args.ForceDestroy),
		Tags:         tags,
	})
	if err != nil {
		return err
	}
	abac, err := s3.NewBucketAbac(ctx, name.Part(abacPart), &s3.BucketAbacArgs{
		Bucket:     bucket.ID(),
		AbacStatus: &s3.BucketAbacAbacStatusArgs{Status: pulumi.String(abacEnabled)},
	}, pulumi.RetainOnDelete(true))
	if err != nil {
		return err
	}
	afterABAC := pulumi.DependsOn([]pulumi.Resource{abac})

	if _, err := s3.NewBucketPublicAccessBlock(ctx, name.Part(publicAccessBlockPart), &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, afterABAC); err != nil {
		return err
	}

	if len(args.CORS.AllowedOrigins) > 0 || args.PatchedCORS {
		rules := s3.BucketCorsConfigurationV2CorsRuleArray{}
		if len(args.CORS.AllowedOrigins) > 0 {
			rules = append(rules, &s3.BucketCorsConfigurationV2CorsRuleArgs{
				AllowedMethods: pulumi.ToStringArray(args.CORS.AllowedMethods),
				AllowedOrigins: pulumi.ToStringArray(args.CORS.AllowedOrigins),
				AllowedHeaders: pulumi.ToStringArray(args.CORS.AllowedHeaders),
				ExposeHeaders:  pulumi.ToStringArray(args.CORS.ExposeHeaders),
				MaxAgeSeconds:  pulumi.Int(args.CORS.MaxAgeSeconds),
			})
		}
		if _, err := s3.NewBucketCorsConfigurationV2(ctx, name.Part(corsPart), &s3.BucketCorsConfigurationV2Args{
			Bucket:    bucket.ID(),
			CorsRules: rules,
		}, afterABAC); err != nil {
			return err
		}
	}

	completerName := name.Child(uploadCompleterSuffix)
	completerRole, err := component.NewServiceRole(ctx,
		completerName,
		at.Description("execution role for the "+at.Name+" bucket's upload completer"),
		"lambda.amazonaws.com",
		stack.AppBoundaryARN,
		stack.AppRolePath,
		args.Tags,
		map[string]component.PolicyStatement{
			completerS3Policy:       {Actions: args.UploadCompleterS3Actions, Resources: []pulumi.StringInput{component.JoinARN(bucket.Arn, "/*")}},
			completerSessionsPolicy: sessionStatement(args.UploadCompleterSessionActions, sessions),
		})
	if err != nil {
		return err
	}
	if _, err := component.AttachManagedPolicy(ctx, completerName, completerRole, iam.LambdaBasicExecutionPolicyARN); err != nil {
		return err
	}

	completerLogs, err := component.NewLambdaLogGroup(ctx, completerName, naming.KindUploadCompleter, "", args.Tags)
	if err != nil {
		return err
	}

	completer, err := lambda.NewFunction(ctx, completerName.Pulumi, &lambda.FunctionArgs{
		Name:          completerName.AWS,
		Runtime:       pulumi.String(args.UploadCompleterRuntime),
		Handler:       pulumi.String(args.UploadCompleterHandler),
		Role:          completerRole.Arn,
		Timeout:       pulumi.Int(args.UploadCompleterTimeoutSeconds),
		Description:   component.CapDescription(at.Description("upload completer for the "+at.Name+" bucket"), component.MaxDescriptionLen),
		LoggingConfig: component.LambdaLogging(completerLogs),
		Tags:          component.Tags(naming.KindUploadCompleter, "", args.Tags),
		S3Bucket:      pulumi.String(completerCode.Bucket),
		S3Key:         pulumi.String(completerCode.Key),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{
				EnvStateTable:     pulumi.String(stack.StateTable),
				EnvSessionPrefix:  pulumi.String(sessions.KeyPrefix),
				envAllowedOrigins: pulumi.String(strings.Join(args.AllowedOrigins, ",")),
			},
		},
	})
	if err != nil {
		return err
	}

	perm, err := lambda.NewPermission(ctx, completerName.Part(completerInvokePermission), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  completer.Name,
		Principal: pulumi.String("s3.amazonaws.com"),
		SourceArn: bucket.Arn,
	})
	if err != nil {
		return err
	}

	if _, err := s3.NewBucketNotification(ctx, name.Part(notificationPart), &s3.BucketNotificationArgs{
		Bucket: bucket.ID(),
		LambdaFunctions: s3.BucketNotificationLambdaFunctionArray{
			&s3.BucketNotificationLambdaFunctionArgs{
				LambdaFunctionArn: completer.Arn,
				Events:            pulumi.ToStringArray(args.NotificationEvents),
			},
		},
	}, pulumi.DependsOn([]pulumi.Resource{perm, abac})); err != nil {
		return err
	}

	ctx.Export(logicalName, pulumi.Map{OutputKeyBucket: bucket.Bucket})

	return nil
}

func sessionStatement(actions []string, sessions Sessions) component.PolicyStatement {
	return component.PolicyStatement{
		Actions:   actions,
		Resources: []pulumi.StringInput{pulumi.String(sessions.TableARN)},
		Condition: map[string]any{
			"ForAllValues:StringLike": map[string]any{
				"dynamodb:LeadingKeys": []string{sessions.KeyPrefix + "*"},
			},
		},
	}
}

func collectBinding(name string, sessions Sessions, fields map[string]any) (*variablestorev1.PublishedBinding, error) {
	bucket, err := component.RequireStringField(fields, name, OutputKeyBucket)
	if err != nil {
		return nil, err
	}
	if sessions.TableARN == "" {
		return nil, fmt.Errorf("bucket %s keeps its upload sessions in this account's state table, and this deploy resolved no ARN for it", name)
	}
	return &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{Name: name, Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: bucket}}},
		Grants:  Grants(bucket, sessions),
	}, nil
}

const OutputKeyBucket = "bucket"
