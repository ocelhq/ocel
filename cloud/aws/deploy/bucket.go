package deploy

import (
	"encoding/json"
	"strings"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Provider-chosen defaults for a bucket's production upload path. BucketConfig
// only carries allowed_origins this slice, so everything else (CORS methods,
// notification event, Lambda runtime, IAM actions) is fixed here.
const (
	// The browser uploads bytes with a presigned PUT, so the bucket must permit
	// cross-origin PUT (and its preflight) from the app's declared origins.
	bucketCORSMaxAgeSeconds = 3600

	// A single presigned PUT is a create; ObjectCreated:* covers PUT and the
	// multipart completion a later slice may add.
	bucketNotificationEvent = "s3:ObjectCreated:*"

	// The listener ships as a Go custom-runtime Lambda: a `bootstrap` executable
	// under the provided.al2023 runtime.
	listenerRuntime        = "provided.al2023"
	listenerHandler        = "bootstrap"
	listenerTimeoutSeconds = 30

	// Listener Lambda env vars. envStateTable matches the runtime binary's
	// OCEL_RUNTIME_STATE_TABLE so both processes read the same table name;
	// envAllowedOrigins carries the deploy-known callback-origin allowlist.
	envStateTable   = "OCEL_RUNTIME_STATE_TABLE"
	envAllowedOrigins = "OCEL_LISTENER_ALLOWED_ORIGINS"
)

// bucketArgs is the fully-resolved set of arguments a BucketConfig lowers to,
// independent of any Pulumi or AWS call. It is the pure output of
// translateBucket so the translation can be unit-tested without provisioning.
type bucketArgs struct {
	// AllowedOrigins is the app's declared origin list. It drives the bucket CORS
	// AllowedOrigins and, injected into the listener, the callback-origin
	// allowlist — one declaration, both trust boundaries.
	AllowedOrigins []string

	CORS corsRule

	NotificationEvents []string

	ListenerRuntime        string
	ListenerHandler        string
	ListenerTimeoutSeconds int

	// RuntimeS3Actions / RuntimeSessionActions are granted to the runtime
	// process's IAM role: mint presigned PUTs (PutObject) and read/write the
	// session table.
	RuntimeS3Actions      []string
	RuntimeSessionActions []string

	// ListenerS3Actions / ListenerSessionActions are granted to the listener
	// Lambda's role: read the landed object's tags and perform the guarded
	// transition on the session table.
	ListenerS3Actions      []string
	ListenerSessionActions []string
}

// corsRule is the single CORS rule derived from allowed_origins, mapped 1:1 onto
// an S3 BucketCorsConfigurationV2 rule by registerBucket.
type corsRule struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

// translateBucket lowers a BucketConfig into the concrete arguments the provider
// provisions. It is pure: same config in, same args out, no I/O. The app's
// allowed_origins become the CORS allowed origins for the browser PUT (and are
// carried through for the listener's callback-origin allowlist).
func translateBucket(cfg *resourcesv1.BucketConfig) bucketArgs {
	origins := cfg.GetAllowedOrigins()
	return bucketArgs{
		AllowedOrigins: origins,
		CORS: corsRule{
			AllowedOrigins: origins,
			AllowedMethods: []string{"PUT"},
			// Allow any request header so the browser preflight for the presigned
			// PUT's signed headers never fails; expose ETag so the client can read
			// the stored object's ETag off the PUT response.
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

// registerBucket declares one bucket resource's full production upload path in a
// Pulumi program: a private (public-access-blocked) S3 bucket with CORS from
// allowed_origins, an ObjectCreated -> listener Lambda notification, the listener
// Lambda itself, and the two IAM roles (runtime process: S3-presign +
// session-table access; listener: object-tag read + session-table transition).
// stateTableName/stateTableARN identify the account-global sessions table
// bootstrap provisions; listenerCodePath is the built listener handler archive
// (its packaging via provider distribution is deferred — see deploy.Config).
// The bucket name is exported under logicalName for collectBucketOutput.
func registerBucket(ctx *pulumi.Context, logicalName string, args bucketArgs, stateTableName, stateTableARN, listenerCodePath string) (pulumi.StringOutput, error) {
	// The logical name is `<type>_<id>` (underscores); S3 bucket names are
	// DNS-constrained and reject underscores, so name from a safe prefix rather
	// than Pulumi's autoname. bucket.Bucket still resolves to the generated
	// physical name for the exported output.
	bucket, err := s3.NewBucketV2(ctx, logicalName, &s3.BucketV2Args{
		BucketPrefix: pulumi.String(physicalNamePrefix(logicalName, "")),
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Private: block every form of public access. Bytes reach the bucket only
	// through presigned URLs the runtime mints.
	if _, err := s3.NewBucketPublicAccessBlock(ctx, logicalName+"-pab", &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}); err != nil {
		return pulumi.StringOutput{}, err
	}

	if _, err := s3.NewBucketCorsConfigurationV2(ctx, logicalName+"-cors", &s3.BucketCorsConfigurationV2Args{
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

	// The runtime process (membrane-launched app runtime) role: presign PUTs and
	// touch the session table.
	if _, err := newServiceRole(ctx, logicalName+"-runtime", "ec2.amazonaws.com", map[string]policyStatement{
		"s3":       {Actions: args.RuntimeS3Actions, Resources: []pulumi.StringInput{joinArn(bucket.Arn, "/*")}},
		"sessions": {Actions: args.RuntimeSessionActions, Resources: []pulumi.StringInput{pulumi.String(stateTableARN)}},
	}); err != nil {
		return pulumi.StringOutput{}, err
	}

	// The listener Lambda's role: read object tags and perform the guarded
	// transition. It also needs the basic Lambda execution policy for logs.
	listenerRole, err := newServiceRole(ctx, logicalName+"-listener", "lambda.amazonaws.com", map[string]policyStatement{
		"s3":       {Actions: args.ListenerS3Actions, Resources: []pulumi.StringInput{joinArn(bucket.Arn, "/*")}},
		"sessions": {Actions: args.ListenerSessionActions, Resources: []pulumi.StringInput{pulumi.String(stateTableARN)}},
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, logicalName+"-listener-logs", &iam.RolePolicyAttachmentArgs{
		Role:      listenerRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	}); err != nil {
		return pulumi.StringOutput{}, err
	}

	listener, err := lambda.NewFunction(ctx, logicalName+"-listener", &lambda.FunctionArgs{
		Runtime: pulumi.String(args.ListenerRuntime),
		Handler: pulumi.String(args.ListenerHandler),
		Role:    listenerRole.Arn,
		Timeout: pulumi.Int(args.ListenerTimeoutSeconds),
		Code:    pulumi.NewFileArchive(listenerCodePath),
		Environment: &lambda.FunctionEnvironmentArgs{
			Variables: pulumi.StringMap{
				envStateTable:   pulumi.String(stateTableName),
				envAllowedOrigins: pulumi.String(strings.Join(args.AllowedOrigins, ",")),
			},
		},
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Let S3 invoke the listener before wiring the notification, else the
	// notification create races the permission.
	perm, err := lambda.NewPermission(ctx, logicalName+"-invoke", &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  listener.Name,
		Principal: pulumi.String("s3.amazonaws.com"),
		SourceArn: bucket.Arn,
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	if _, err := s3.NewBucketNotification(ctx, logicalName+"-notify", &s3.BucketNotificationArgs{
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

// policyStatement is one inline IAM policy statement: a set of actions on a set
// of resource ARNs.
type policyStatement struct {
	Actions   []string
	Resources []pulumi.StringInput
}

// newServiceRole creates an IAM role assumable by the given service principal,
// with one inline policy per named statement. The policy JSON is built with
// pulumi.All so it resolves the (still-unknown) resource ARNs.
func newServiceRole(ctx *pulumi.Context, name, servicePrincipal string, statements map[string]policyStatement) (*iam.Role, error) {
	role, err := iam.NewRole(ctx, name, &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(assumeRolePolicy(servicePrincipal)),
	})
	if err != nil {
		return nil, err
	}
	for stmtName, stmt := range statements {
		actions := stmt.Actions
		resourceInputs := make([]interface{}, len(stmt.Resources))
		for i, r := range stmt.Resources {
			resourceInputs[i] = r
		}
		policyJSON := pulumi.All(resourceInputs...).ApplyT(func(vs []interface{}) (string, error) {
			resources := make([]string, len(vs))
			for i, v := range vs {
				resources[i], _ = v.(string)
			}
			return inlinePolicy(actions, resources)
		}).(pulumi.StringOutput)

		if _, err := iam.NewRolePolicy(ctx, name+"-"+stmtName, &iam.RolePolicyArgs{
			Role:   role.ID(),
			Policy: policyJSON,
		}); err != nil {
			return nil, err
		}
	}
	return role, nil
}

// assumeRolePolicy renders the trust policy allowing servicePrincipal to assume
// the role.
func assumeRolePolicy(servicePrincipal string) string {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{{
			"Effect":    "Allow",
			"Action":    "sts:AssumeRole",
			"Principal": map[string]interface{}{"Service": servicePrincipal},
		}},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// inlinePolicy renders an inline IAM policy granting actions on resources.
func inlinePolicy(actions, resources []string) (string, error) {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{{
			"Effect":   "Allow",
			"Action":   actions,
			"Resource": resources,
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// joinArn appends a literal suffix (e.g. "/*") to an ARN output.
func joinArn(arn pulumi.StringOutput, suffix string) pulumi.StringInput {
	return arn.ApplyT(func(a string) string { return a + suffix }).(pulumi.StringOutput)
}

// collectBucketOutput builds the BucketOutput for a provisioned bucket. bucket
// is the real S3 bucket name from the stack; address is the runtime endpoint the
// app dials. address is a deferred placeholder: its live value is the local
// socket the membrane serves BucketService on, and the membrane's
// launch/address-injection lands separately. It is populated coherently so the
// env payload shape ({address, bucket}) is complete, and re-pointed when the
// membrane lands.
func collectBucketOutput(name string, fields map[string]interface{}) (*deploymentsv1.ResourceOutput, error) {
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

// deferredRuntimeAddress is the placeholder BucketOutput.address until the
// membrane lands: the membrane spawns the app and serves BucketService over
// this local socket, so the SDK's injected OCEL_RESOURCE_BUCKET_<id>.address is
// this value. It is NOT wired to a running deployment in this slice.
const deferredRuntimeAddress = "unix:///run/ocel/runtime.sock"

// outputKeyBucket is the key registerBucket exports the provisioned bucket name
// under, read back by collectBucketOutput.
const outputKeyBucket = "bucket"
