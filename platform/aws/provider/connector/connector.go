package connector

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	Arch = "arm64"

	runtime    = "provided.al2023"
	handler    = "bootstrap"
	memory     = 256
	timeout    = 30
	codePrefix = "connector"
	codeAbort  = 7

	outputBucket  = "CodeBucketName"
	outputURL     = "FunctionUrl"
	outputVersion = "ConnectorVersion"
)

func StackName(ns bootstrap.Namespace) string { return string(ns) + "-connector" }

type APIs struct {
	CFN     cfn.API
	Buckets cfn.BucketEmptierAPI
	Objects payloads.ObjectStore
}

type Standing struct {
	Present bool
	Version string
	URL     string
}

type Release struct {
	Binary  []byte
	Version string
	Config  []byte
}

func Read(ctx context.Context, api cfn.Describer, ns bootstrap.Namespace) (Standing, error) {
	stack, err := cfn.DescribeStack(ctx, api, StackName(ns))
	if err != nil || stack == nil {
		return Standing{}, err
	}
	if cfn.Unusable(stack.StackStatus) {
		return Standing{}, nil
	}
	outputs := cfn.OutputsOf(stack)
	return Standing{
		Present: outputs[outputURL] != "",
		Version: outputs[outputVersion],
		URL:     outputs[outputURL],
	}, nil
}

func Install(ctx context.Context, apis APIs, ns bootstrap.Namespace, release Release,
	writer providerkit.Writer, progress func(string)) (string, error) {
	bucket, err := codeBucket(ctx, apis, ns, writer, progress)
	if err != nil {
		return "", err
	}

	archived, err := zipped(release.Binary)
	if err != nil {
		return "", err
	}
	at, err := payloads.Place(ctx, apis.Objects, bucket, codePrefix, "connector", archived)
	if err != nil {
		return "", err
	}
	say(progress, "connector "+release.Version+" staged at "+at.Key)

	template, err := templateFor(ns, at, release.Version, release.Config)
	if err != nil {
		return "", err
	}
	if err := cfn.Upsert(ctx, apis.CFN, ns, StackName(ns), template, nil,
		[]cfntypes.Capability{cfntypes.CapabilityCapabilityIam}, tagsFor(ns, template, writer), nil); err != nil {
		return "", err
	}

	standing, err := Read(ctx, apis.CFN, ns)
	if err != nil {
		return "", err
	}
	if standing.URL == "" {
		return "", providerkit.Refuse(providerkit.CodeNotReady,
			"%s stands and published no function url, so the console has nothing to dial", StackName(ns))
	}
	return standing.URL, nil
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
	return nil
}

func codeBucket(ctx context.Context, apis APIs, ns bootstrap.Namespace,
	writer providerkit.Writer, progress func(string)) (string, error) {
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
			return "", providerkit.Refuse(providerkit.CodeInvalid,
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
		return "", providerkit.Refuse(providerkit.CodeNotReady,
			"%s opened and named no bucket to stage the connector's code in", StackName(ns))
	}
	return opened[outputBucket], nil
}

func tagsFor(ns bootstrap.Namespace, template string, writer providerkit.Writer) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(cfn.TagNamespace), Value: aws.String(string(ns))},
		{Key: aws.String(cfn.TagDigest), Value: aws.String(cfn.TemplateDigest(template))},
		{Key: aws.String(cfn.TagBootstrappedBy), Value: aws.String(string(writer))},
	}
}

func zipped(binary []byte) (payloads.Payload, error) {
	if len(binary) == 0 {
		return payloads.Payload{}, providerkit.Refuse(providerkit.CodeInvalid,
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

func templateFor(ns bootstrap.Namespace, at payloads.Placement, version string, config []byte) (string, error) {
	if len(config) == 0 {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"this install carries no connector config, so nothing would name the console the function trusts")
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
						"PolicyDocument": map[string]any{"Version": "2012-10-17", "Statement": statements(ns)},
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
						providerkit.ConnectorConfigEnvVar: string(config),
					}},
					"Handler":    handler,
					"MemorySize": memory,
					"Role":       map[string]any{"Fn::GetAtt": []string{"ConnectorRole", "Arn"}},
					"Runtime":    runtime,
					"Timeout":    timeout,
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
				"Value":       version,
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

func tier(ns bootstrap.Namespace) []bootstrap.GrantStatement {
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
			Resources: []string{bootstrap.AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + bootstrap.VarsKeyComponentTagKey: bootstrap.VarsKeyComponentTagValue},
			},
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

func statements(ns bootstrap.Namespace) []map[string]any {
	return bootstrap.PolicyStatements(tier(ns))
}
