package deploy

import (
	"encoding/json"
	"fmt"
	"strings"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	bucketCORSMaxAgeSeconds = 3600

	maxS3BucketNameLen   = 63
	s3AutonameSuffixLen  = 26
	maxS3BucketPrefixLen = maxS3BucketNameLen - s3AutonameSuffixLen

	bucketNotificationEvent = "s3:ObjectCreated:*"

	uploadCompleterRuntime        = "provided.al2023"
	uploadCompleterHandler        = "bootstrap"
	uploadCompleterTimeoutSeconds = 30

	envStateTable     = "OCEL_RUNTIME_STATE_TABLE"
	envSessionPrefix  = "OCEL_RUNTIME_SESSION_PREFIX"
	envAllowedOrigins = "OCEL_UPLOAD_COMPLETER_ALLOWED_ORIGINS"
)

type sessionScope struct {
	TableARN  string
	KeyPrefix string
}

func newSessionScope(project, env, tableARN string) sessionScope {
	return sessionScope{TableARN: tableARN, KeyPrefix: naming.SessionKeyPrefix(project, env)}
}

type bucketArgs struct {
	AllowedOrigins []string

	ForceDestroy bool

	Tags map[string]string

	CORS corsRule

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

func translateBucket(spec *providerkit.BucketSpec) bucketArgs {
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

func resourceCoordinate(project, env, logicalName string, kind naming.Kind) naming.Coordinate {
	name := logicalName
	if _, local, ok := strings.Cut(logicalName, naming.FieldSeparator); ok {
		name = local
	}
	return naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: kind, Name: name}
}

func registerBucket(ctx *pulumi.Context, project, env, logicalName string, args bucketArgs, stateTableName, boundaryARN string, sessions sessionScope, completerCode payloads.Placement) error {
	at := resourceCoordinate(project, env, logicalName, naming.KindBucket)

	bucket, err := s3.NewBucketV2(ctx, naming.ResourceID(at.Kind, at.Name), &s3.BucketV2Args{
		BucketPrefix: pulumi.String(at.PhysicalPrefix(maxS3BucketPrefixLen)),
		ForceDestroy: pulumi.Bool(args.ForceDestroy),
		Tags:         resourceTags(at.Kind, "", args.Tags),
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

	if _, err := s3.NewBucketCorsConfigurationV2(ctx, naming.ResourceID(at.Kind, at.Name, "cors"), &s3.BucketCorsConfigurationV2Args{
		Bucket: bucket.ID(),
		CorsRules: s3.BucketCorsConfigurationV2CorsRuleArray{
			&s3.BucketCorsConfigurationV2CorsRuleArgs{
				AllowedMethods: pulumi.ToStringArray(args.CORS.AllowedMethods),
				AllowedOrigins: pulumi.ToStringArray(args.CORS.AllowedOrigins),
				AllowedHeaders: pulumi.ToStringArray(args.CORS.AllowedHeaders),
				ExposeHeaders:  pulumi.ToStringArray(args.CORS.ExposeHeaders),
				MaxAgeSeconds:  pulumi.Int(args.CORS.MaxAgeSeconds),
			},
		},
	}); err != nil {
		return err
	}

	completerRole, err := newServiceRole(ctx,
		naming.ResourceID(at.Kind, at.Name, "upload-completer-role"),
		at.Description("execution role for the "+at.Name+" bucket's upload completer"),
		"lambda.amazonaws.com",
		boundaryARN,
		args.Tags,
		map[string]policyStatement{
			"s3":       {Actions: args.UploadCompleterS3Actions, Resources: []pulumi.StringInput{joinArn(bucket.Arn, "/*")}},
			"sessions": sessionStatement(args.UploadCompleterSessionActions, sessions),
		})
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(at.Kind, at.Name, "upload-completer-logs-policy"), &iam.RolePolicyAttachmentArgs{
		Role:      completerRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return err
	}

	completer, err := lambda.NewFunction(ctx, naming.ResourceID(at.Kind, at.Name, "upload-completer"), &lambda.FunctionArgs{
		Runtime:     pulumi.String(args.UploadCompleterRuntime),
		Handler:     pulumi.String(args.UploadCompleterHandler),
		Role:        completerRole.Arn,
		Timeout:     pulumi.Int(args.UploadCompleterTimeoutSeconds),
		Description: capDescription(at.Description("upload completer for the "+at.Name+" bucket"), maxDescriptionLen),
		Tags:        resourceTags(naming.KindUploadCompleter, "", args.Tags),
		S3Bucket:    pulumi.String(completerCode.Bucket),
		S3Key:       pulumi.String(completerCode.Key),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{
				envStateTable:     pulumi.String(stateTableName),
				envSessionPrefix:  pulumi.String(sessions.KeyPrefix),
				envAllowedOrigins: pulumi.String(strings.Join(args.AllowedOrigins, ",")),
			},
		},
	})
	if err != nil {
		return err
	}

	perm, err := lambda.NewPermission(ctx, naming.ResourceID(at.Kind, at.Name, "upload-completer-permission"), &lambda.PermissionArgs{
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

	ctx.Export(logicalName, pulumi.Map{outputKeyBucket: bucket.Bucket})

	return nil
}

type policyStatement struct {
	Actions   []string
	Resources []pulumi.StringInput
	Condition map[string]any
}

func sessionStatement(actions []string, sessions sessionScope) policyStatement {
	return policyStatement{
		Actions:   actions,
		Resources: []pulumi.StringInput{pulumi.String(sessions.TableARN)},
		Condition: map[string]any{
			"ForAllValues:StringLike": map[string]any{
				"dynamodb:LeadingKeys": []string{sessions.KeyPrefix + "*"},
			},
		},
	}
}

func newServiceRole(ctx *pulumi.Context, name, description, servicePrincipal, boundaryARN string, tags map[string]string, statements map[string]policyStatement) (*iam.Role, error) {
	role, err := iam.NewRole(ctx, name, &iam.RoleArgs{
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(servicePrincipal)),
		Description:         pulumi.String(description),
		PermissionsBoundary: permissionsBoundary(boundaryARN),
		Tags:                resourceTags(naming.KindRole, "", tags),
	})
	if err != nil {
		return nil, err
	}
	for stmtName, stmt := range statements {
		actions, condition := stmt.Actions, stmt.Condition
		resourceInputs := make([]any, len(stmt.Resources))
		for i, r := range stmt.Resources {
			resourceInputs[i] = r
		}
		policyJSON := pulumi.All(resourceInputs...).ApplyT(func(vs []any) (string, error) {
			resources := make([]string, len(vs))
			for i, v := range vs {
				resources[i], _ = v.(string)
			}
			return inlinePolicy(actions, resources, condition)
		}).(pulumi.StringOutput)

		if _, err := iam.NewRolePolicy(ctx, name+"-policy-"+stmtName, &iam.RolePolicyArgs{
			Role:   role.ID(),
			Policy: policyJSON,
		}); err != nil {
			return nil, err
		}
	}
	return role, nil
}

func permissionsBoundary(arn string) pulumi.StringPtrInput {
	if arn == "" {
		return nil
	}
	return pulumi.StringPtr(arn)
}

func assumeRolePolicy(servicePrincipal string) string {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Action":    "sts:AssumeRole",
			"Principal": map[string]any{"Service": servicePrincipal},
		}},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func inlinePolicy(actions, resources []string, condition map[string]any) (string, error) {
	statement := map[string]any{
		"Effect":   "Allow",
		"Action":   actions,
		"Resource": resources,
	}
	if len(condition) > 0 {
		statement["Condition"] = condition
	}
	doc := map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{statement},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func joinArn(arn pulumi.StringOutput, suffix string) pulumi.StringInput {
	return arn.ApplyT(func(a string) string { return a + suffix }).(pulumi.StringOutput)
}

func collectBucketLink(name string, sessions sessionScope, fields map[string]any) (*linksv1.Link, error) {
	bucket, err := requireStringField(fields, name, outputKeyBucket)
	if err != nil {
		return nil, err
	}
	if sessions.TableARN == "" {
		return nil, fmt.Errorf("bucket %s keeps its upload sessions in this account's state table, and this deploy resolved no ARN for it", name)
	}
	return &linksv1.Link{
		Name:       name,
		Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: bucket}},
		Grants:     bucketGrants(bucket, sessions),
	}, nil
}

const outputKeyBucket = "bucket"
