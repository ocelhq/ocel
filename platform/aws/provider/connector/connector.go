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

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	Arch = "arm64"

	runtime = "provided.al2023"
	handler = "bootstrap"
	memory  = 256
	timeout = 30

	reservedConcurrency = 10
	codePrefix          = "connector"
	codeAbort           = 7

	outputBucket    = "CodeBucketName"
	outputURL       = "FunctionUrl"
	outputVersion   = "ConnectorVersion"
	outputPublicKey = "ConnectorPublicKey"

	KeyParameterEnvVar = "OCEL_CONNECTOR_KEY_PARAMETER"

	heartbeatEvery = "rate(1 minute)"
)

func StackName(ns bootstrap.Namespace) string { return string(ns) + "-connector" }

func KeyParameter(ns bootstrap.Namespace) string { return "/" + string(ns) + "/connector/key" }

type Wake struct {
	Ocel string `json:"ocel"`
}

const WakeHeartbeat = "heartbeat"

func reviewing(ns bootstrap.Namespace, progress func(string)) cfn.ChangeReview {
	return bootstrap.AdmitReplacements(ns, false, progress)
}

type APIs struct {
	CFN     cfn.API
	Buckets cfn.BucketEmptierAPI
	Objects payloads.ObjectStore
	SSM     KeyStore
}

type KeyStore interface {
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

func mintKey(ctx context.Context, store KeyStore, ns bootstrap.Namespace) (string, error) {
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

func takeKey(ctx context.Context, store KeyStore, ns bootstrap.Namespace) error {
	_, err := store.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(KeyParameter(ns))})
	var gone *ssmtypes.ParameterNotFound
	if err != nil && !errors.As(err, &gone) {
		return fmt.Errorf("delete the connector's key %s: %w", KeyParameter(ns), err)
	}
	return nil
}

type Standing struct {
	Present   bool
	Version   string
	URL       string
	PublicKey string
}

type Release struct {
	Binary    []byte
	Version   string
	Config    []byte
	PublicKey string
}

func Read(ctx context.Context, api cfn.StacksAPI, ns bootstrap.Namespace) (Standing, error) {
	stack, err := cfn.DescribeStack(ctx, api, StackName(ns))
	if err != nil || stack == nil {
		return Standing{}, err
	}
	if cfn.Unusable(stack.StackStatus) {
		return Standing{}, nil
	}
	outputs := cfn.OutputsOf(stack)
	return Standing{
		Present:   outputs[outputURL] != "",
		Version:   outputs[outputVersion],
		URL:       outputs[outputURL],
		PublicKey: outputs[outputPublicKey],
	}, nil
}

func varsKeys(ctx context.Context, api cfn.StacksAPI, ns bootstrap.Namespace) ([]string, error) {
	held := make([]string, 0, 2)
	for _, class := range []string{bootstrap.ClassProduction, bootstrap.ClassPreview} {
		deployed, err := bootstrap.CheckDeployedFor(ctx, api, ns, class)
		if err != nil {
			return nil, err
		}
		if !deployed.Present || deployed.VarsKeyARN == "" {
			continue
		}
		if !slices.Contains(held, deployed.VarsKeyARN) {
			held = append(held, deployed.VarsKeyARN)
		}
	}
	if len(held) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"neither the %s nor the %s class of %s is bootstrapped with a key variables are sealed under, so the connector would read nothing: bootstrap this account and run ocel connector add again",
			bootstrap.ClassProduction, bootstrap.ClassPreview, ns)
	}
	return held, nil
}

func Install(ctx context.Context, apis APIs, ns bootstrap.Namespace, release Release,
	writer providerkit.WrittenBy, progress func(string)) (Standing, error) {
	keys, err := varsKeys(ctx, apis.CFN, ns)
	if err != nil {
		return Standing{}, err
	}

	bucket, err := codeBucket(ctx, apis, ns, writer, progress)
	if err != nil {
		return Standing{}, err
	}

	archived, err := zipped(release.Binary)
	if err != nil {
		return Standing{}, err
	}
	at, err := payloads.Place(ctx, apis.Objects, bucket, codePrefix, "connector", archived)
	if err != nil {
		return Standing{}, err
	}
	say(progress, "connector "+release.Version+" staged at "+at.Key)

	if release.PublicKey, err = mintKey(ctx, apis.SSM, ns); err != nil {
		return Standing{}, err
	}
	say(progress, "the connector's key is at "+KeyParameter(ns))

	template, err := templateFor(ns, at, release, keys)
	if err != nil {
		return Standing{}, err
	}
	if err := cfn.Upsert(ctx, apis.CFN, ns.ChangeSetNameFor, StackName(ns), template, nil,
		[]cfntypes.Capability{cfntypes.CapabilityCapabilityIam}, tagsFor(ns, template, writer),
		reviewing(ns, progress)); err != nil {
		return Standing{}, err
	}

	standing, err := Read(ctx, apis.CFN, ns)
	if err != nil {
		return Standing{}, err
	}
	if standing.URL == "" {
		return Standing{}, refusal.Refuse(refusal.CodeNotReady,
			"%s stands and published no function url, so the console has nothing to dial", StackName(ns))
	}
	return standing, nil
}

func Remove(ctx context.Context, apis APIs, ns bootstrap.Namespace, progress func(string)) error {
	stack, err := cfn.DescribeStack(ctx, apis.CFN, StackName(ns))
	if err != nil {
		return err
	}
	if stack == nil {
		say(progress, StackName(ns)+" does not stand")
		return nil
	}
	if bucket := cfn.OutputsOf(stack)[outputBucket]; bucket != "" {
		if err := cfn.EmptyBucket(ctx, apis.Buckets, bucket); err != nil {
			return err
		}
		say(progress, "emptied "+bucket)
	}
	if err := cfn.Delete(ctx, apis.CFN, StackName(ns)); err != nil {
		return err
	}
	say(progress, "deleted "+StackName(ns))
	if err := takeKey(ctx, apis.SSM, ns); err != nil {
		return err
	}
	say(progress, "deleted "+KeyParameter(ns))
	return nil
}

func codeBucket(ctx context.Context, apis APIs, ns bootstrap.Namespace,
	writer providerkit.WrittenBy, progress func(string)) (string, error) {
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
				"%s stands and names no bucket to stage the connector's code in: delete that stack and run this again",
				StackName(ns))
		}
		return bucket, nil
	}

	template := codeTemplate()
	say(progress, "opening "+StackName(ns)+" to stage the connector's code in")
	if err := cfn.Create(ctx, apis.CFN, StackName(ns), template, nil, nil,
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

func tagsFor(ns bootstrap.Namespace, template string, writer providerkit.WrittenBy) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(cfn.TagNamespace), Value: aws.String(string(ns))},
		{Key: aws.String(cfn.TagDigest), Value: aws.String(cfn.TemplateDigest(template))},
		{Key: aws.String(cfn.TagBootstrappedBy), Value: aws.String(string(writer))},
	}
}

func zipped(binary []byte) (payloads.Payload, error) {
	if len(binary) == 0 {
		return payloads.Payload{}, refusal.Refuse(refusal.CodeInvalid,
			"this install carries no connector binary to put on a function")
	}
	var held bytes.Buffer
	archive := zip.NewWriter(&held)
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
	return payloads.Of(held.Bytes()), nil
}

func codeTemplate() string {
	rendered, err := json.MarshalIndent(map[string]any{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Description":              "Ocel connector - the bucket the connector's own code is staged in. The function that reads it lands in this same stack once the code is there.",
		"Resources":                map[string]any{"CodeBucket": codeBucketResource()},
		"Outputs": map[string]any{
			outputBucket: map[string]any{
				"Description": "S3 bucket this stack stages the connector's code in. Nothing else writes to it.",
				"Value":       map[string]any{"Ref": "CodeBucket"},
			},
		},
	}, "", "  ")
	if err != nil {
		panic("connector: render the connector code template: " + err.Error())
	}
	return string(rendered)
}

func templateFor(ns bootstrap.Namespace, at payloads.Placement, release Release, keys []string) (string, error) {
	if len(release.Config) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install carries no connector config, so nothing would name the console the function trusts")
	}
	if len(keys) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install names no key variables are sealed under, so the connector would reach every key in the account")
	}
	if release.PublicKey == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install names no public key for the connector, and the console verifies every heartbeat against one")
	}
	wake, err := json.Marshal(Wake{Ocel: WakeHeartbeat})
	if err != nil {
		return "", err
	}
	rendered, err := json.MarshalIndent(map[string]any{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Description":              "Ocel connector - the function the console reads this account's variables through, the role it runs under and the url it answers on. It stands apart from every bootstrap stack and carries no deploy rights.",
		"Resources": map[string]any{
			"CodeBucket": codeBucketResource(),
			"ConnectorRole": map[string]any{
				"Type": "AWS::IAM::Role",
				"Metadata": map[string]any{
					"Description": "What the connector may reach: the variable records and the key they are sealed under, and nothing a deploy would need.",
				},
				"Properties": map[string]any{
					"AssumeRolePolicyDocument": map[string]any{
						"Version": "2012-10-17",
						"Statement": []map[string]any{{
							"Effect":    "Allow",
							"Principal": map[string]any{"Service": bootstrap.LambdaServicePrincipal},
							"Action":    "sts:AssumeRole",
						}},
					},
					"ManagedPolicyArns": []string{bootstrap.LambdaBasicExecutionPolicyARN},
					"Policies": []map[string]any{{
						"PolicyName":     ns.PolicyName("connector"),
						"PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": statements(ns, keys)},
					}},
				},
			},
			"ConnectorFunction": map[string]any{
				"Type": "AWS::Lambda::Function",
				"Metadata": map[string]any{
					"Description": "The connector itself. The console dials it over its function url and authenticates with a short-lived token the connector verifies before any call runs.",
				},
				"Properties": map[string]any{
					"Architectures": []string{Arch},
					"Code":          map[string]any{"S3Bucket": map[string]any{"Ref": "CodeBucket"}, "S3Key": at.Key},
					"Environment": map[string]any{"Variables": map[string]any{
						providerkit.NamespaceEnvVar:       string(ns),
						edge.AWSRegionVar:                 map[string]any{"Ref": "AWS::Region"},
						providerkit.ConnectorConfigEnvVar: string(release.Config),
						KeyParameterEnvVar:                KeyParameter(ns),
					}},
					"Handler":                      handler,
					"MemorySize":                   memory,
					"ReservedConcurrentExecutions": reservedConcurrency,
					"Role":                         map[string]any{"Fn::GetAtt": []string{"ConnectorRole", "Arn"}},
					"Runtime":                      runtime,
					"Timeout":                      timeout,
				},
			},
			"ConnectorUrl": map[string]any{
				"Type": "AWS::Lambda::Url",
				"Metadata": map[string]any{
					"Description": "The address the console dials. Its auth is the token the connector checks itself, so a caller with no token reaches a refusal rather than a signed request it could never make.",
				},
				"Properties": map[string]any{
					"AuthType":          "NONE",
					"TargetFunctionArn": map[string]any{"Fn::GetAtt": []string{"ConnectorFunction", "Arn"}},
				},
			},
			"ConnectorUrlReachable": map[string]any{
				"Type": "AWS::Lambda::Permission",
				"Properties": map[string]any{
					"Action":              "lambda:InvokeFunctionUrl",
					"FunctionName":        map[string]any{"Fn::GetAtt": []string{"ConnectorFunction", "Arn"}},
					"FunctionUrlAuthType": "NONE",
					"Principal":           "*",
				},
			},
			"ConnectorHeartbeat": map[string]any{
				"Type": "AWS::Events::Rule",
				"Metadata": map[string]any{
					"Description": "Wakes the connector once a minute to send the console a signed heartbeat: a function runs nothing between invocations, so the beat has to be one.",
				},
				"Properties": map[string]any{
					"ScheduleExpression": heartbeatEvery,
					"State":              "ENABLED",
					"Targets": []map[string]any{{
						"Id":    "connector",
						"Arn":   map[string]any{"Fn::GetAtt": []string{"ConnectorFunction", "Arn"}},
						"Input": string(wake),
					}},
				},
			},
			"ConnectorHeartbeatInvokes": map[string]any{
				"Type": "AWS::Lambda::Permission",
				"Properties": map[string]any{
					"Action":       "lambda:InvokeFunction",
					"FunctionName": map[string]any{"Fn::GetAtt": []string{"ConnectorFunction", "Arn"}},
					"Principal":    "events.amazonaws.com",
					"SourceArn":    map[string]any{"Fn::GetAtt": []string{"ConnectorHeartbeat", "Arn"}},
				},
			},
		},
		"Outputs": map[string]any{
			outputBucket: map[string]any{
				"Description": "S3 bucket this stack stages the connector's code in. Nothing else writes to it.",
				"Value":       map[string]any{"Ref": "CodeBucket"},
			},
			outputURL: map[string]any{
				"Description": "The url the console dials this connector at.",
				"Value":       map[string]any{"Fn::GetAtt": []string{"ConnectorUrl", "FunctionUrl"}},
			},
			outputVersion: map[string]any{
				"Description": "The connector release this stack carries.",
				"Value":       release.Version,
			},
			outputPublicKey: map[string]any{
				"Description": "The public half of the key the connector signs its heartbeats with. The private half is the SecureString parameter the function reads.",
				"Value":       release.PublicKey,
			},
		},
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render the connector template: %w", err)
	}
	return string(rendered), nil
}

func codeBucketResource() map[string]any {
	return map[string]any{
		"Type": "AWS::S3::Bucket",
		"Metadata": map[string]any{
			"Description": "Holds the connector's own code archive, keyed by its digest. Emptying it leaves the standing function running and the next install re-uploads.",
		},
		"Properties": map[string]any{
			"BucketEncryption": map[string]any{
				"ServerSideEncryptionConfiguration": []map[string]any{{
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
				"Rules": []map[string]any{{
					"Id":                             "abort-incomplete-connector-uploads",
					"Status":                         "Enabled",
					"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": codeAbort},
				}},
			},
		},
	}
}

func say(progress func(string), message string) {
	if progress != nil {
		progress(message)
	}
}

func tier(ns bootstrap.Namespace, keys []string) []bootstrap.GrantStatement {
	r := ns.ScopedARNs()
	return []bootstrap.GrantStatement{
		{
			Actions: []string{
				"dynamodb:DeleteItem",
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:Query",
				"dynamodb:TransactWriteItems",
			},
			Resources: []string{r.BootstrapTable, r.BootstrapTablePart},
		},
		{
			Actions:   []string{"kms:Decrypt", "kms:Encrypt"},
			Resources: keys,
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + bootstrap.VarsKeyComponentTagKey: bootstrap.VarsKeyComponentTagValue},
			},
		},
		{
			Actions:   []string{"ssm:GetParameter"},
			Resources: []string{"arn:aws:ssm:*:*:parameter" + KeyParameter(ns)},
		},
		{
			Actions:   []string{"cloudformation:DescribeStacks"},
			Resources: []string{r.BootstrapStack},
		},
		{
			Actions:   []string{"sts:GetCallerIdentity"},
			Resources: []string{bootstrap.UnscopedResource},
		},
	}
}

func statements(ns bootstrap.Namespace, keys []string) []map[string]any {
	return bootstrap.PolicyStatements(tier(ns, keys))
}
