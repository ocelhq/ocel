package bucket

import (
	"fmt"
	"strings"

	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
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

	maxS3BucketNameLen   = 63
	s3AutonameSuffixLen  = 26
	maxS3BucketPrefixLen = maxS3BucketNameLen - s3AutonameSuffixLen

	bucketNotificationEvent = "s3:ObjectCreated:*"

	uploadCompleterLocalName      = "upload-completer"
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

	Tier environment.Tier

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

func bucketPrefix(tier environment.Tier, at naming.Coordinate) string {
	scope := iam.TierScopePrefix(tier)
	return scope + at.PhysicalPrefix(maxS3BucketPrefixLen-len(scope))
}

func register(ctx *pulumi.Context, project, env, logicalName string, args bucketArgs, stateTableName, boundaryARN, rolePath string, sessions Sessions, completerCode payloads.Placement) error {
	at := component.ResourceCoordinate(project, env, logicalName, naming.KindBucket)
	if boundaryARN == "" {
		return fmt.Errorf("bucket %s: this deploy resolved no app boundary, and its upload completer's role made without one is capped by nothing", at.Name)
	}

	bucket, err := s3.NewBucketV2(ctx, naming.ResourceID(at.Kind, at.Name), &s3.BucketV2Args{
		BucketPrefix: pulumi.String(bucketPrefix(args.Tier, at)),
		ForceDestroy: pulumi.Bool(args.ForceDestroy),
		Tags:         component.Tags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	if _, err := s3.NewBucketPublicAccessBlock(ctx, naming.ResourceID(at.Kind, at.Name, "public-access-block"), &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}); err != nil {
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
		if _, err := s3.NewBucketCorsConfigurationV2(ctx, naming.ResourceID(at.Kind, at.Name, "cors"), &s3.BucketCorsConfigurationV2Args{
			Bucket:    bucket.ID(),
			CorsRules: rules,
		}); err != nil {
			return err
		}
	}

	completerRole, err := component.NewServiceRole(ctx,
		naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-role"),
		at.Description("execution role for the "+at.Name+" bucket's upload completer"),
		"lambda.amazonaws.com",
		boundaryARN,
		rolePath,
		args.Tags,
		map[string]component.PolicyStatement{
			"s3":       {Actions: args.UploadCompleterS3Actions, Resources: []pulumi.StringInput{component.JoinARN(bucket.Arn, "/*")}},
			"sessions": sessionStatement(args.UploadCompleterSessionActions, sessions),
		})
	if err != nil {
		return err
	}
	if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs-policy"), &awsiam.RolePolicyAttachmentArgs{
		Role:      completerRole.Name,
		PolicyArn: pulumi.String(iam.LambdaBasicExecutionPolicyARN),
	}); err != nil {
		return err
	}

	completerLogs, err := component.NewLambdaLogGroup(ctx,
		naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs"),
		at.PhysicalPrefix(component.MaxLambdaBaseNameLen-len(uploadCompleterLocalName))+uploadCompleterLocalName,
		naming.KindUploadCompleter, "", args.Tags)
	if err != nil {
		return err
	}

	completer, err := lambda.NewFunction(ctx, naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName), &lambda.FunctionArgs{
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
				EnvStateTable:     pulumi.String(stateTableName),
				EnvSessionPrefix:  pulumi.String(sessions.KeyPrefix),
				envAllowedOrigins: pulumi.String(strings.Join(args.AllowedOrigins, ",")),
			},
		},
	})
	if err != nil {
		return err
	}

	perm, err := lambda.NewPermission(ctx, naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-permission"), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  completer.Name,
		Principal: pulumi.String("s3.amazonaws.com"),
		SourceArn: bucket.Arn,
	})
	if err != nil {
		return err
	}

	if _, err := s3.NewBucketNotification(ctx, naming.ResourceID(at.Kind, at.Name, "notification"), &s3.BucketNotificationArgs{
		Bucket: bucket.ID(),
		LambdaFunctions: s3.BucketNotificationLambdaFunctionArray{
			&s3.BucketNotificationLambdaFunctionArgs{
				LambdaFunctionArn: completer.Arn,
				Events:            pulumi.ToStringArray(args.NotificationEvents),
			},
		},
	}, pulumi.DependsOn([]pulumi.Resource{perm})); err != nil {
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
