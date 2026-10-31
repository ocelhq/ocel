package connector

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	OS   = "linux"
	Arch = "arm64"

	runtime = "provided.al2023"
	handler = "bootstrap"
	memory  = 256
	timeout = 30

	codeBucketID        = "CodeBucket"
	functionID          = "ConnectorFunction"
	logGroupPrefix      = "/aws/lambda/"
	reservedConcurrency = 10
	unreservedMinimum   = 100
	codePayload         = "connector"
	codeAbort           = 7

	outputBucket    = "CodeBucketName"
	outputURL       = "FunctionUrl"
	outputVersion   = "ConnectorVersion"
	outputPublicKey = "ConnectorPublicKey"

	KeyParameterEnvVar = "OCEL_CONNECTOR_KEY_PARAMETER"
	RegionEnvVar       = "OCEL_AWS_REGION"

	heartbeatEvery = "rate(1 minute)"
)

func StackName(ns account.Namespace) string {
	return naming.MustUntieredOcelName(string(ns), "connector")
}

func KeyParameter(ns account.Namespace) string { return "/" + string(ns) + "/connector/key" }

type Wake struct {
	Ocel string `json:"ocel"`
}

const WakeHeartbeat = "heartbeat"

var installCapabilities = []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}

func reviewing(progress progress.Log) cfn.ChangeReview {
	return func(stackName string, changes []cfntypes.ResourceChange) error {
		for _, change := range changes {
			if aws.ToString(change.LogicalResourceId) != codeBucketID {
				continue
			}
			if change.Replacement == cfntypes.ReplacementTrue || change.Replacement == cfntypes.ReplacementConditional {
				return fmt.Errorf("writing %s would replace %s, the bucket the connector's code is staged in, and the function's code would go with it: delete the connector and add it again", stackName, codeBucketID)
			}
		}
		if progress != nil {
			for _, change := range changes {
				if change.Replacement == cfntypes.ReplacementTrue {
					progress.Say(fmt.Sprintf("Replacing %s, which holds nothing but what this install writes again", aws.ToString(change.LogicalResourceId)))
				}
			}
		}
		return nil
	}
}

type APIs struct {
	CFN     cfn.API
	Buckets cfn.BucketEmptierAPI
	Objects payloads.ObjectStore
	SSM     KeyStore
	Lambda  AccountSettingsAPI
	Logs    LogGroupsAPI
	Region  string
}

type LogGroupsAPI interface {
	DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
}

type AccountSettingsAPI interface {
	GetAccountSettings(ctx context.Context, in *lambda.GetAccountSettingsInput, optFns ...func(*lambda.Options)) (*lambda.GetAccountSettingsOutput, error)
}

func reservationFor(ctx context.Context, api AccountSettingsAPI) (int32, error) {
	settings, err := api.GetAccountSettings(ctx, &lambda.GetAccountSettingsInput{})
	if err != nil {
		return 0, fmt.Errorf("read the account's Lambda concurrency: %w", err)
	}
	if settings.AccountLimit == nil || settings.AccountLimit.UnreservedConcurrentExecutions == nil {
		return 0, nil
	}
	if *settings.AccountLimit.UnreservedConcurrentExecutions-reservedConcurrency < unreservedMinimum {
		return 0, nil
	}
	return reservedConcurrency, nil
}

func stagedTemplate(ctx context.Context, apis APIs, bucket, body string) (cfn.TemplateSource, error) {
	if len(body) <= cfn.TemplateBodyBytes {
		return cfn.TemplateSource{Body: body}, nil
	}
	key := "templates/" + cfn.TemplateDigest(body) + ".json"
	if _, err := apis.Objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(body),
		ContentType: aws.String("application/json"),
	}); err != nil {
		return cfn.TemplateSource{}, fmt.Errorf("stage the connector's template in %s: %w", bucket, err)
	}
	return cfn.TemplateSource{URL: "https://" + bucket + ".s3." + apis.Region + ".amazonaws.com/" + key}, nil
}

type KeyStore interface {
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

func mintKey(ctx context.Context, store KeyStore, ns account.Namespace) (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return "", fmt.Errorf("mint the connector's key: %w", err)
	}
	if _, err := store.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(KeyParameter(ns)),
		Description: aws.String("Ocel: the key the connector signs its heartbeats to the console with. The function reads it at start; nothing else does."),
		Value:       aws.String(base64.StdEncoding.EncodeToString(seed)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return "", fmt.Errorf("write the connector's key into %s: %w", KeyParameter(ns), err)
	}
	return PublicKeyOf(seed), nil
}

func PublicKeyOf(seed []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func takeKey(ctx context.Context, store KeyStore, ns account.Namespace) error {
	_, err := store.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(KeyParameter(ns))})
	var gone *ssmtypes.ParameterNotFound
	if err != nil && !errors.As(err, &gone) {
		return fmt.Errorf("delete the connector's key %s: %w", KeyParameter(ns), err)
	}
	return nil
}

type Installation struct {
	Present   bool
	Version   string
	URL       string
	PublicKey string
}

type Bundle struct {
	Binary    []byte
	Version   string
	Config    []byte
	PublicKey string
	Policies  []iam.ManagedPolicy
}

func Read(ctx context.Context, api cfn.StacksAPI, ns account.Namespace) (Installation, error) {
	stack, err := cfn.DescribeStack(ctx, api, StackName(ns))
	if err != nil || stack == nil {
		return Installation{}, err
	}
	if cfn.Unusable(stack.StackStatus) {
		return Installation{}, nil
	}
	outputs := cfn.OutputsOf(stack)
	return Installation{
		Present:   outputs[outputURL] != "",
		Version:   outputs[outputVersion],
		URL:       outputs[outputURL],
		PublicKey: outputs[outputPublicKey],
	}, nil
}

func variablesKeys(ctx context.Context, api cfn.StacksAPI, ns account.Namespace) ([]string, error) {
	keys := make([]string, 0, 2)
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		deployed, err := account.CheckDeployedFor(ctx, api, ns, tier)
		if err != nil {
			return nil, err
		}
		if !deployed.Present || deployed.VariablesKeyARN == "" {
			continue
		}
		if !slices.Contains(keys, deployed.VariablesKeyARN) {
			keys = append(keys, deployed.VariablesKeyARN)
		}
	}
	if len(keys) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"neither the %s nor the %s tier of %s is bootstrapped with a key variables are sealed under, so the connector would read nothing: bootstrap this account and run ocel connector add again",
			environment.TierProduction, environment.TierPreview, ns)
	}
	return keys, nil
}

func Install(ctx context.Context, apis APIs, ns account.Namespace, bundle Bundle,
	writer provider.WrittenBy, progress progress.Log) (Installation, error) {
	keys, err := variablesKeys(ctx, apis.CFN, ns)
	if err != nil {
		return Installation{}, err
	}

	bucket, err := codeBucket(ctx, apis, ns, writer, progress)
	if err != nil {
		return Installation{}, err
	}

	archived, err := zipped(bundle.Binary)
	if err != nil {
		return Installation{}, err
	}
	at, err := payloads.Place(ctx, apis.Objects, bucket, codePayload, "connector", archived)
	if err != nil {
		return Installation{}, err
	}
	progress.Say(fmt.Sprintf("Staged connector %s at s3://%s/%s", bundle.Version, bucket, at.Key))

	if bundle.PublicKey, err = mintKey(ctx, apis.SSM, ns); err != nil {
		return Installation{}, err
	}
	progress.Say(fmt.Sprintf("Stored the connector's key in parameter %s", KeyParameter(ns)))

	reserved, err := reservationFor(ctx, apis.Lambda)
	if err != nil {
		return Installation{}, err
	}
	template, err := templateFor(ns, at, bundle, keys, reserved)
	if err != nil {
		return Installation{}, err
	}
	source, err := stagedTemplate(ctx, apis, bucket, template)
	if err != nil {
		return Installation{}, err
	}
	if err := cfn.Upsert(ctx, apis.CFN, ns.ChangeSetNameFor, StackName(ns), source, nil,
		installCapabilities, tagsFor(ns, template, writer),
		reviewing(progress)); err != nil {
		return Installation{}, err
	}

	installed, err := Read(ctx, apis.CFN, ns)
	if err != nil {
		return Installation{}, err
	}
	if installed.URL == "" {
		return Installation{}, refusal.Refuse(refusal.CodeNotReady,
			"%s is deployed and published no function url, so the console has nothing to dial", StackName(ns))
	}
	return installed, nil
}

func Remove(ctx context.Context, apis APIs, ns account.Namespace, progress progress.Log) error {
	stack, err := cfn.DescribeStack(ctx, apis.CFN, StackName(ns))
	if err != nil {
		return err
	}
	if stack == nil {
		progress.Say(fmt.Sprintf("Nothing to remove: stack %s does not exist", StackName(ns)))
		return nil
	}
	if bucket := cfn.OutputsOf(stack)[outputBucket]; bucket != "" {
		if err := cfn.EmptyBucket(ctx, apis.Buckets, bucket); err != nil {
			return err
		}
		progress.Say(fmt.Sprintf("Emptied bucket %s, where the connector's code was staged", bucket))
	}
	function, err := readFunctionName(ctx, apis.CFN, ns)
	if err != nil {
		return err
	}
	if err := cfn.Delete(ctx, apis.CFN, StackName(ns)); err != nil {
		return err
	}
	progress.Say("Deleted stack " + StackName(ns))
	if err := deleteLogGroup(ctx, apis.Logs, function, progress); err != nil {
		return err
	}
	if err := takeKey(ctx, apis.SSM, ns); err != nil {
		return err
	}
	progress.Say(fmt.Sprintf("Deleted parameter %s, the connector's key", KeyParameter(ns)))
	return nil
}

func readFunctionName(ctx context.Context, api cfn.API, ns account.Namespace) (string, error) {
	pages := cloudformation.NewListStackResourcesPaginator(api, &cloudformation.ListStackResourcesInput{StackName: aws.String(StackName(ns))})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("read the function of stack %s: %w", StackName(ns), err)
		}
		for _, resource := range page.StackResourceSummaries {
			if aws.ToString(resource.LogicalResourceId) == functionID {
				return aws.ToString(resource.PhysicalResourceId), nil
			}
		}
	}
	return "", nil
}

func deleteLogGroup(ctx context.Context, api LogGroupsAPI, function string, progress progress.Log) error {
	if function == "" {
		return nil
	}
	group := logGroupPrefix + function
	_, err := api.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	var missing *logstypes.ResourceNotFoundException
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete log group %s: %w", group, err)
	}
	progress.Say("Deleted log group " + group)
	return nil
}

func codeBucket(ctx context.Context, apis APIs, ns account.Namespace,
	writer provider.WrittenBy, progress progress.Log) (string, error) {
	stack, err := cfn.DescribeStack(ctx, apis.CFN, StackName(ns))
	if err != nil {
		return "", err
	}
	if stack != nil && cfn.Unusable(stack.StackStatus) {
		if bucket := cfn.OutputsOf(stack)[outputBucket]; bucket != "" {
			if err := cfn.EmptyBucket(ctx, apis.Buckets, bucket); err != nil {
				return "", err
			}
		}
		if err := cfn.Delete(ctx, apis.CFN, StackName(ns)); err != nil {
			return "", err
		}
		stack = nil
	}
	if stack != nil {
		bucket := cfn.OutputsOf(stack)[outputBucket]
		if bucket == "" {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"%s exists and names no bucket to stage the connector's code in: delete that stack and run this again",
				StackName(ns))
		}
		return bucket, nil
	}

	template, err := codeTemplate()
	if err != nil {
		return "", err
	}
	progress.Say(fmt.Sprintf("Creating stack %s to stage the connector's code in", StackName(ns)))
	if err := cfn.Create(ctx, apis.CFN, StackName(ns), cfn.TemplateSource{Body: template}, nil, nil,
		tagsFor(ns, template, writer)); err != nil {
		return "", err
	}
	opened, err := cfn.StackOutputs(ctx, apis.CFN, StackName(ns))
	if err != nil {
		return "", err
	}
	if opened[outputBucket] == "" {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"%s opened and named no bucket to stage the connector's code in", StackName(ns))
	}
	return opened[outputBucket], nil
}

func tagsFor(ns account.Namespace, template string, writer provider.WrittenBy) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(cfn.TagNamespace), Value: aws.String(string(ns))},
		{Key: aws.String(cfn.TagDigest), Value: aws.String(cfn.TemplateDigest(template))},
		{Key: aws.String(cfn.TagBootstrappedBy), Value: aws.String(string(writer))},
	}
}

func zipped(binary []byte) (payloads.Payload, error) {
	if len(binary) == 0 {
		return payloads.Payload{}, refusal.Refuse(refusal.CodeInvalid,
			"this install ships no connector binary to put on a function")
	}
	var body bytes.Buffer
	archive := zip.NewWriter(&body)
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: handler, Method: zip.Deflate})
	if err != nil {
		return payloads.Payload{}, fmt.Errorf("open the connector archive: %w", err)
	}
	if _, err := entry.Write(binary); err != nil {
		return payloads.Payload{}, fmt.Errorf("write the connector into its archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return payloads.Payload{}, fmt.Errorf("close the connector archive: %w", err)
	}
	return payloads.Of(body.Bytes()), nil
}

func codeBucketOutput() cfn.Output {
	return cfn.Output{
		Name:        outputBucket,
		Description: "S3 bucket this stack stages the connector's code in. Nothing else writes to it.",
		Value:       cfn.Ref("CodeBucket"),
	}
}

func codeTemplate() (string, error) {
	return cfn.Template{
		Description: "Ocel connector - the bucket the connector's own code is staged in. The function that reads it lands in this same stack once the code is there.",
		Resources:   []cfn.Resource{codeBucketResource()},
		Outputs:     []cfn.Output{codeBucketOutput()},
	}.Body()
}

func templateFor(ns account.Namespace, at payloads.Placement, bundle Bundle, keys []string, reserved int32) (string, error) {
	if len(bundle.Config) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install ships no connector config, so nothing would name the console the function trusts")
	}
	if len(keys) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install names no key variables are sealed under, so the connector would reach every key in the account")
	}
	if bundle.PublicKey == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install names no public key for the connector, and the console verifies every heartbeat against one")
	}
	wake, err := json.Marshal(Wake{Ocel: WakeHeartbeat})
	if err != nil {
		return "", err
	}
	functionARN := cfn.GetAtt(functionID, "Arn")
	managed := []any{iam.LambdaBasicExecutionPolicyARN}
	resources := []cfn.Resource{codeBucketResource()}
	for i, policy := range bundle.Policies {
		id := "ConnectorPolicy" + strconv.Itoa(i+1)
		managed = append(managed, cfn.Ref(id))
		resources = append(resources, cfn.Resource{
			ID:   id,
			Type: "AWS::IAM::ManagedPolicy",
			Metadata: cfn.Metadata{
				Description: "What the control calls the connector serves reach, scoped as the deploy credential scopes them.",
			},
			Properties: map[string]any{
				"ManagedPolicyName": policy.PolicyName,
				"PolicyDocument":    policy.PolicyDocument,
			},
		})
	}
	resources = append(resources, []cfn.Resource{
		{
			ID:   "ConnectorRole",
			Type: "AWS::IAM::Role",
			Metadata: cfn.Metadata{
				Description: "What the connector may reach: the variable records, the key they are sealed under, and its control policies.",
			},
			Properties: map[string]any{
				"AssumeRolePolicyDocument": iam.AssumeRoleDocument(iam.LambdaPrincipal),
				"ManagedPolicyArns":        managed,
				"Policies": []any{map[string]any{
					"PolicyName":     ns.PolicyName("connector"),
					"PolicyDocument": iam.Document(grants(ns, keys)),
				}},
			},
		},
		{
			ID:   functionID,
			Type: "AWS::Lambda::Function",
			Metadata: cfn.Metadata{
				Description: "The connector itself. The console dials it over its function url and authenticates with a short-lived token the connector verifies before any call runs.",
			},
			Properties: functionProperties(reserved, map[string]any{
				"Architectures": []string{Arch},
				"Code":          map[string]any{"S3Bucket": cfn.Ref(codeBucketID), "S3Key": at.Key},
				"Environment": map[string]any{"Variables": map[string]any{
					provider.NamespaceEnvVar: string(ns),
					RegionEnvVar:             cfn.Ref("AWS::Region"),
					endpoint.ConfigEnvVar:    string(bundle.Config),
					KeyParameterEnvVar:       KeyParameter(ns),
				}},
				"Handler":    handler,
				"MemorySize": memory,
				"Role":       cfn.GetAtt("ConnectorRole", "Arn"),
				"Runtime":    runtime,
				"Timeout":    timeout,
			}),
		},
		{
			ID:   "ConnectorUrl",
			Type: "AWS::Lambda::Url",
			Metadata: cfn.Metadata{
				Description: "The address the console dials. Its auth is the token the connector checks itself, so a caller with no token reaches a refusal rather than a signed request it could never make.",
			},
			Properties: map[string]any{
				"AuthType":          functionurl.AuthNone,
				"TargetFunctionArn": functionARN,
			},
		},
		{
			ID:   "ConnectorHeartbeat",
			Type: "AWS::Events::Rule",
			Metadata: cfn.Metadata{
				Description: "Wakes the connector once a minute to send the console a signed heartbeat: a function runs nothing between invocations, so the beat has to be one.",
			},
			Properties: map[string]any{
				"ScheduleExpression": heartbeatEvery,
				"State":              "ENABLED",
				"Targets": []any{map[string]any{
					"Id":    "connector",
					"Arn":   functionARN,
					"Input": string(wake),
				}},
			},
		},
		{
			ID:   "ConnectorHeartbeatInvokes",
			Type: "AWS::Lambda::Permission",
			Properties: map[string]any{
				"Action":       "lambda:InvokeFunction",
				"FunctionName": functionARN,
				"Principal":    "events.amazonaws.com",
				"SourceArn":    cfn.GetAtt("ConnectorHeartbeat", "Arn"),
			},
		},
	}...)
	return cfn.Template{
		Description: "Ocel connector - the function the console reaches this account through, the role and policies it runs under and the url it answers on. It is separate from every bootstrap stack.",
		Resources:   append(resources, urlPermissions(functionARN)...),
		Outputs: []cfn.Output{
			codeBucketOutput(),
			{
				Name:        outputURL,
				Description: "The url the console dials this connector at.",
				Value:       cfn.GetAtt("ConnectorUrl", "FunctionUrl"),
			},
			{
				Name:        outputVersion,
				Description: "The connector bundle this stack deploys.",
				Value:       bundle.Version,
			},
			{
				Name:        outputPublicKey,
				Description: "The public half of the key the connector signs its heartbeats with. The private half is the SecureString parameter the function reads.",
				Value:       bundle.PublicKey,
			},
		},
	}.CompactBody()
}

func urlPermissionID(permission functionurl.Permission) string {
	return "ConnectorUrl" + permission.Verb()
}

func urlPermissions(functionARN cfn.Intrinsic) []cfn.Resource {
	var resources []cfn.Resource
	for _, permission := range functionurl.Public() {
		properties := map[string]any{
			"Action":       permission.Action,
			"FunctionName": functionARN,
			"Principal":    "*",
		}
		if permission.URLAuthType != "" {
			properties["FunctionUrlAuthType"] = permission.URLAuthType
		}
		if permission.ViaURL {
			properties["InvokedViaFunctionUrl"] = true
		}
		resources = append(resources, cfn.Resource{ID: urlPermissionID(permission), Type: "AWS::Lambda::Permission", Properties: properties})
	}
	return resources
}

func codeBucketResource() cfn.Resource {
	return cfn.Resource{
		ID:   codeBucketID,
		Type: "AWS::S3::Bucket",
		Metadata: cfn.Metadata{
			Description: "Stores the connector's own code archive, keyed by its digest. Emptying it leaves the deployed function running and the next install re-uploads.",
		},
		Properties: map[string]any{
			"BucketEncryption": map[string]any{
				"ServerSideEncryptionConfiguration": []any{map[string]any{
					"ServerSideEncryptionByDefault": map[string]any{"SSEAlgorithm": "AES256"},
				}},
			},
			"PublicAccessBlockConfiguration": map[string]any{
				"BlockPublicAcls":       true,
				"BlockPublicPolicy":     true,
				"IgnorePublicAcls":      true,
				"RestrictPublicBuckets": true,
			},
			"LifecycleConfiguration": map[string]any{
				"Rules": []any{map[string]any{
					"Id":                             "abort-incomplete-connector-uploads",
					"Status":                         "Enabled",
					"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": codeAbort},
				}},
			},
		},
	}
}

func grants(ns account.Namespace, keys []string) []iam.Statement {
	production, preview := ns.Scope(environment.TierProduction), ns.Scope(environment.TierPreview)
	return []iam.Statement{
		{
			Actions: []string{
				"dynamodb:DeleteItem",
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:Query",
				"dynamodb:TransactWriteItems",
			},
			Resources: slices.Concat(production.BootstrapTables, preview.BootstrapTables),
		},
		{
			Actions:   []string{"kms:Decrypt", "kms:Encrypt"},
			Resources: keys,
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + account.VariablesKeyComponentTagKey: account.VariablesKeyComponentTagValue},
			},
		},
		{
			Actions:   []string{"ssm:GetParameter"},
			Resources: []string{"arn:aws:ssm:*:*:parameter" + KeyParameter(ns)},
		},
		{
			Actions:   []string{"cloudformation:DescribeStacks"},
			Resources: slices.Concat(production.BootstrapStacks, preview.BootstrapStacks),
		},
		{
			Actions:   []string{"sts:GetCallerIdentity"},
			Resources: []string{iam.AnyResource},
		},
	}
}

func functionProperties(reserved int32, properties map[string]any) map[string]any {
	if reserved > 0 {
		properties["ReservedConcurrentExecutions"] = reserved
	}
	return properties
}
