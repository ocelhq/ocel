package deploy

import (
	"encoding/json"
	"strings"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const (
	bucketCORSMaxAgeSeconds = 3600

	maxS3BucketNameLen   = 63
	s3AutonameSuffixLen  = 26
	maxS3BucketPrefixLen = maxS3BucketNameLen - s3AutonameSuffixLen

	bucketNotificationEvent = "s3:ObjectCreated:*"

	listenerRuntime        = "provided.al2023"
	listenerHandler        = "bootstrap"
	listenerTimeoutSeconds = 30

	envStateTable     = "OCEL_RUNTIME_STATE_TABLE"
	envAllowedOrigins = "OCEL_LISTENER_ALLOWED_ORIGINS"
)

type bucketArgs struct {
	AllowedOrigins []string

	CORS corsRule

	NotificationEvents []string

	ListenerRuntime        string
	ListenerHandler        string
	ListenerTimeoutSeconds int

	RuntimeS3Actions      []string
	RuntimeSessionActions []string

	ListenerS3Actions      []string
	ListenerSessionActions []string
}

type corsRule struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

func translateBucket(cfg *resourcesv1.BucketConfig) bucketArgs {
	origins := cfg.GetAllowedOrigins()
	return bucketArgs{
		AllowedOrigins: origins,
		CORS: corsRule{
			AllowedOrigins: origins,
			AllowedMethods: []string{"PUT"},
			AllowedHeaders: []string{"*"},
			ExposeHeaders:  []string{"ETag"},
			MaxAgeSeconds:  bucketCORSMaxAgeSeconds,
		},
		NotificationEvents:     []string{bucketNotificationEvent},
		ListenerRuntime:        listenerRuntime,
		ListenerHandler:        listenerHandler,
		ListenerTimeoutSeconds: listenerTimeoutSeconds,
		RuntimeS3Actions:       []string{"s3:PutObject"},
		RuntimeSessionActions:  []string{"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:Query"},
		ListenerS3Actions:      []string{"s3:GetObjectTagging"},
		ListenerSessionActions: []string{"dynamodb:GetItem", "dynamodb:UpdateItem"},
	}
}

func resourceCoordinate(project, env, logicalName string, kind naming.Kind) naming.Coordinate {
	name := logicalName
	if _, local, ok := strings.Cut(logicalName, naming.FieldSeparator); ok {
		name = local
	}
	return naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: kind, Name: name}
}

func registerBucket(ctx *pulumi.Context, project, env, logicalName string, args bucketArgs, stateTableName, stateTableARN, listenerCodePath string) (pulumi.StringOutput, error) {
	at := resourceCoordinate(project, env, logicalName, naming.KindBucket)

	bucket, err := s3.NewBucketV2(ctx, naming.ResourceID(at.Kind, at.Name), &s3.BucketV2Args{
		BucketPrefix: pulumi.String(at.PhysicalPrefix(maxS3BucketPrefixLen)),
		Tags:         resourceTags(at.Kind, ""),
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	if _, err := s3.NewBucketPublicAccessBlock(ctx, naming.ResourceID(at.Kind, at.Name, "public-access-block"), &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}); err != nil {
		return pulumi.StringOutput{}, err
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
		return pulumi.StringOutput{}, err
	}

	if _, err := newServiceRole(ctx,
		naming.ResourceID(at.Kind, at.Name, "runtime-role"),
		at.Description("runtime role for the "+at.Name+" bucket, granting presigned uploads and session bookkeeping"),
		"ec2.amazonaws.com",
		map[string]policyStatement{
			"s3":       {Actions: args.RuntimeS3Actions, Resources: []pulumi.StringInput{joinArn(bucket.Arn, "/*")}},
			"sessions": sessionStatement(args.RuntimeSessionActions, stateTableARN),
		}); err != nil {
		return pulumi.StringOutput{}, err
	}

	listenerRole, err := newServiceRole(ctx,
		naming.ResourceID(at.Kind, at.Name, "event-listener-role"),
		at.Description("execution role for the "+at.Name+" bucket's upload event listener"),
		"lambda.amazonaws.com",
		map[string]policyStatement{
			"s3":       {Actions: args.ListenerS3Actions, Resources: []pulumi.StringInput{joinArn(bucket.Arn, "/*")}},
			"sessions": sessionStatement(args.ListenerSessionActions, stateTableARN),
		})
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(at.Kind, at.Name, "event-listener-logs-policy"), &iam.RolePolicyAttachmentArgs{
		Role:      listenerRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return pulumi.StringOutput{}, err
	}

	listener, err := lambda.NewFunction(ctx, naming.ResourceID(at.Kind, at.Name, "event-listener"), &lambda.FunctionArgs{
		Runtime:     pulumi.String(args.ListenerRuntime),
		Handler:     pulumi.String(args.ListenerHandler),
		Role:        listenerRole.Arn,
		Timeout:     pulumi.Int(args.ListenerTimeoutSeconds),
		Description: pulumi.String(at.Description("upload event listener for the " + at.Name + " bucket")),
		Tags:        resourceTags(naming.KindListener, ""),
		Code:        pulumi.NewFileArchive(listenerCodePath),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{
				envStateTable:     pulumi.String(stateTableName),
				envAllowedOrigins: pulumi.String(strings.Join(args.AllowedOrigins, ",")),
			},
		},
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	perm, err := lambda.NewPermission(ctx, naming.ResourceID(at.Kind, at.Name, "event-listener-permission"), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  listener.Name,
		Principal: pulumi.String("s3.amazonaws.com"),
		SourceArn: bucket.Arn,
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	if _, err := s3.NewBucketNotification(ctx, naming.ResourceID(at.Kind, at.Name, "notification"), &s3.BucketNotificationArgs{
		Bucket: bucket.ID(),
		LambdaFunctions: s3.BucketNotificationLambdaFunctionArray{
			&s3.BucketNotificationLambdaFunctionArgs{
				LambdaFunctionArn: listener.Arn,
				Events:            pulumi.ToStringArray(args.NotificationEvents),
			},
		},
	}, pulumi.DependsOn([]pulumi.Resource{perm})); err != nil {
		return pulumi.StringOutput{}, err
	}

	ctx.Export(logicalName, pulumi.Map{outputKeyBucket: bucket.Bucket})

	return bucketEnvValue(bucket.Bucket), nil
}

type policyStatement struct {
	Actions   []string
	Resources []pulumi.StringInput
	Condition map[string]any
}

const sessionKeyPrefix = "SESSION#"

func sessionStatement(actions []string, stateTableARN string) policyStatement {
	return policyStatement{
		Actions:   actions,
		Resources: []pulumi.StringInput{pulumi.String(stateTableARN)},
		Condition: map[string]any{
			"ForAllValues:StringLike": map[string]any{
				"dynamodb:LeadingKeys": []string{sessionKeyPrefix + "*"},
			},
		},
	}
}

func newServiceRole(ctx *pulumi.Context, name, description, servicePrincipal string, statements map[string]policyStatement) (*iam.Role, error) {
	role, err := iam.NewRole(ctx, name, &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(assumeRolePolicy(servicePrincipal)),
		Description:      pulumi.String(description),
		Tags:             resourceTags(naming.KindRole, ""),
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

func collectBucketOutput(name string, fields map[string]any) (*deploymentsv1.ResourceOutput, error) {
	bucket, err := requireStringField(fields, name, outputKeyBucket)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ResourceOutput{
		LogicalName: name,
		Output: &deploymentsv1.ResourceOutput_Bucket{
			Bucket: &deploymentsv1.BucketOutput{
				Address: deferredRuntimeAddress,
				Bucket:  bucket,
			},
		},
	}, nil
}

const deferredRuntimeAddress = "unix:///run/ocel/runtime.sock"

const outputKeyBucket = "bucket"
