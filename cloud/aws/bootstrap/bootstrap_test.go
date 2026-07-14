package bootstrap

import (
	"context"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"
)

// parsedTemplate is the subset of the rendered bootstrap template the tests
// assert against.
type parsedTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			BillingMode          string `yaml:"BillingMode"`
			AttributeDefinitions []struct {
				AttributeName string `yaml:"AttributeName"`
				AttributeType string `yaml:"AttributeType"`
			} `yaml:"AttributeDefinitions"`
			KeySchema []struct {
				AttributeName string `yaml:"AttributeName"`
				KeyType       string `yaml:"KeyType"`
			} `yaml:"KeySchema"`
			TimeToLiveSpecification struct {
				AttributeName string `yaml:"AttributeName"`
				Enabled       bool   `yaml:"Enabled"`
			} `yaml:"TimeToLiveSpecification"`
			PublicAccessBlockConfiguration struct {
				BlockPublicAcls       bool `yaml:"BlockPublicAcls"`
				BlockPublicPolicy     bool `yaml:"BlockPublicPolicy"`
				IgnorePublicAcls      bool `yaml:"IgnorePublicAcls"`
				RestrictPublicBuckets bool `yaml:"RestrictPublicBuckets"`
			} `yaml:"PublicAccessBlockConfiguration"`
			LifecycleConfiguration struct {
				Rules []struct {
					Id                             string `yaml:"Id"`
					Status                         string `yaml:"Status"`
					ExpirationInDays               int    `yaml:"ExpirationInDays"`
					AbortIncompleteMultipartUpload struct {
						DaysAfterInitiation int `yaml:"DaysAfterInitiation"`
					} `yaml:"AbortIncompleteMultipartUpload"`
				} `yaml:"Rules"`
			} `yaml:"LifecycleConfiguration"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func parseTemplate(t *testing.T) parsedTemplate {
	t.Helper()
	return parseTemplateStr(t, stackTemplate())
}

func parseTemplateStr(t *testing.T, template string) parsedTemplate {
	t.Helper()
	var tmpl parsedTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

// TestStackTemplate_SessionsTable asserts the bootstrap template provisions the
// account-global sessions table with exactly the schema the runtime expects:
// partition key session_id (S), no sort key, and a TTL on expires_at.
func TestStackTemplate_SessionsTable(t *testing.T) {
	tmpl := parseTemplate(t)

	table, ok := tmpl.Resources["SessionsTable"]
	if !ok {
		t.Fatal("template is missing the SessionsTable resource")
	}
	if table.Type != "AWS::DynamoDB::Table" {
		t.Errorf("SessionsTable Type = %q, want AWS::DynamoDB::Table", table.Type)
	}
	if table.Properties.BillingMode != "PAY_PER_REQUEST" {
		t.Errorf("BillingMode = %q, want PAY_PER_REQUEST", table.Properties.BillingMode)
	}

	if got := table.Properties.AttributeDefinitions; len(got) != 1 || got[0].AttributeName != "session_id" || got[0].AttributeType != "S" {
		t.Errorf("AttributeDefinitions = %+v, want single session_id (S)", got)
	}
	if got := table.Properties.KeySchema; len(got) != 1 || got[0].AttributeName != "session_id" || got[0].KeyType != "HASH" {
		t.Errorf("KeySchema = %+v, want single session_id HASH (no sort key)", got)
	}

	ttl := table.Properties.TimeToLiveSpecification
	if ttl.AttributeName != "expires_at" || !ttl.Enabled {
		t.Errorf("TimeToLiveSpecification = %+v, want expires_at enabled", ttl)
	}

	if _, ok := tmpl.Outputs[outputSessionTable]; !ok {
		t.Errorf("template is missing the %s output", outputSessionTable)
	}
}

// TestArtifactBucket asserts both substrate templates provision the dedicated
// function-artifact bucket with the public-access lockdown the state bucket
// uses and a 30-day expiration lifecycle rule (plus incomplete-multipart abort)
// so churned deploy artifacts don't accrue storage cost. It also asserts the
// bucket name is exported for the deploy path to consume.
func TestArtifactBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate()},
		{"preview", previewStackTemplate()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			bucket, ok := tmpl.Resources["ArtifactBucket"]
			if !ok {
				t.Fatal("template is missing the ArtifactBucket resource")
			}
			if bucket.Type != "AWS::S3::Bucket" {
				t.Errorf("ArtifactBucket Type = %q, want AWS::S3::Bucket", bucket.Type)
			}

			pab := bucket.Properties.PublicAccessBlockConfiguration
			if !pab.BlockPublicAcls || !pab.BlockPublicPolicy || !pab.IgnorePublicAcls || !pab.RestrictPublicBuckets {
				t.Errorf("ArtifactBucket PublicAccessBlockConfiguration = %+v, want all four blocks true", pab)
			}

			rules := bucket.Properties.LifecycleConfiguration.Rules
			if len(rules) != 1 {
				t.Fatalf("ArtifactBucket lifecycle rules = %d, want exactly 1", len(rules))
			}
			rule := rules[0]
			if rule.Status != "Enabled" {
				t.Errorf("lifecycle rule Status = %q, want Enabled", rule.Status)
			}
			if rule.ExpirationInDays != artifactExpirationDays {
				t.Errorf("lifecycle rule ExpirationInDays = %d, want %d", rule.ExpirationInDays, artifactExpirationDays)
			}
			if rule.AbortIncompleteMultipartUpload.DaysAfterInitiation != artifactAbortMultipartDays {
				t.Errorf("lifecycle rule AbortIncompleteMultipartUpload = %d, want %d", rule.AbortIncompleteMultipartUpload.DaysAfterInitiation, artifactAbortMultipartDays)
			}

			if _, ok := tmpl.Outputs[outputArtifactBucket]; !ok {
				t.Errorf("template is missing the %s output", outputArtifactBucket)
			}
		})
	}
}

// TestAssetBucket asserts both substrate templates provision the account-global
// asset bucket that prerender configs + fallbacks are uploaded to. Unlike the
// artifact bucket, assets are keyed by an immutable build id and a live build's
// assets are never re-touched by later deploys, so the bucket carries NO
// object-expiration rule (only an incomplete-multipart abort) and its name is
// exported for the deploy path to consume.
func TestAssetBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate()},
		{"preview", previewStackTemplate()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			bucket, ok := tmpl.Resources["AssetBucket"]
			if !ok {
				t.Fatal("template is missing the AssetBucket resource")
			}
			if bucket.Type != "AWS::S3::Bucket" {
				t.Errorf("AssetBucket Type = %q, want AWS::S3::Bucket", bucket.Type)
			}

			pab := bucket.Properties.PublicAccessBlockConfiguration
			if !pab.BlockPublicAcls || !pab.BlockPublicPolicy || !pab.IgnorePublicAcls || !pab.RestrictPublicBuckets {
				t.Errorf("AssetBucket PublicAccessBlockConfiguration = %+v, want all four blocks true", pab)
			}

			rules := bucket.Properties.LifecycleConfiguration.Rules
			if len(rules) != 1 {
				t.Fatalf("AssetBucket lifecycle rules = %d, want exactly 1", len(rules))
			}
			rule := rules[0]
			if rule.Status != "Enabled" {
				t.Errorf("lifecycle rule Status = %q, want Enabled", rule.Status)
			}
			if rule.ExpirationInDays != 0 {
				t.Errorf("lifecycle rule ExpirationInDays = %d, want 0 (no object expiry)", rule.ExpirationInDays)
			}
			if rule.AbortIncompleteMultipartUpload.DaysAfterInitiation != artifactAbortMultipartDays {
				t.Errorf("lifecycle rule AbortIncompleteMultipartUpload = %d, want %d", rule.AbortIncompleteMultipartUpload.DaysAfterInitiation, artifactAbortMultipartDays)
			}

			if _, ok := tmpl.Outputs[outputAssetBucket]; !ok {
				t.Errorf("template is missing the %s output", outputAssetBucket)
			}
		})
	}
}

// TestStackTemplate_VersionOutput proves the deployed version output is
// single-sourced from RequiredBootstrapVersion, so the two never drift.
func TestStackTemplate_VersionOutput(t *testing.T) {
	tmpl := parseTemplate(t)
	if got := tmpl.Outputs[outputVersion].Value; got != strconv.Itoa(RequiredBootstrapVersion) {
		t.Errorf("%s output = %q, want %d", outputVersion, got, RequiredBootstrapVersion)
	}
}

// stubDescriber returns a fixed DescribeStacks response.
type stubDescriber struct {
	out *cloudformation.DescribeStacksOutput
}

func (s stubDescriber) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return s.out, nil
}

// TestCheckDeployed_ParsesOutputs proves the discovery path surfaces the
// session table name, state bucket, version, and class marker that later
// deploys and the class guard depend on.
func TestCheckDeployed_ParsesOutputs(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputStateBucket), OutputValue: aws.String("bucket-123")},
				{OutputKey: aws.String(outputSessionTable), OutputValue: aws.String("sessions-abc")},
				{OutputKey: aws.String(outputArtifactBucket), OutputValue: aws.String("artifacts-xyz")},
				{OutputKey: aws.String(outputAssetBucket), OutputValue: aws.String("assets-xyz")},
				{OutputKey: aws.String(outputVersion), OutputValue: aws.String("3")},
				{OutputKey: aws.String(outputInfraClass), OutputValue: aws.String(ClassProduction)},
			},
		}},
	}}

	got, err := CheckDeployed(context.Background(), api)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	want := Deployed{Present: true, Version: 3, StateBucket: "bucket-123", SessionTable: "sessions-abc", ArtifactBucket: "artifacts-xyz", AssetBucket: "assets-xyz", Class: ClassProduction}
	if got != want {
		t.Errorf("CheckDeployed = %+v, want %+v", got, want)
	}
}

// TestCheckDeployed_ReadsPreviewClassMarker proves the class stamped on a
// substrate is read back, so the class guard can verify a command acts on the
// right one.
func TestCheckDeployed_ReadsPreviewClassMarker(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputInfraClass), OutputValue: aws.String(ClassPreview)},
			},
		}},
	}}

	got, err := CheckDeployed(context.Background(), api)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if !got.Present || got.Class != ClassPreview {
		t.Errorf("CheckDeployed = %+v, want Present with Class %q", got, ClassPreview)
	}
}

// TestPreviewStackTemplate_StampsPreviewClass proves the preview template stamps
// the preview class marker so CheckDeployedPreview surfaces it.
func TestPreviewStackTemplate_StampsPreviewClass(t *testing.T) {
	var tmpl parsedTemplate
	if err := yaml.Unmarshal([]byte(previewStackTemplate()), &tmpl); err != nil {
		t.Fatalf("preview template is not valid YAML: %v", err)
	}
	if got := tmpl.Outputs[outputInfraClass].Value; got != ClassPreview {
		t.Errorf("%s output = %q, want %q", outputInfraClass, got, ClassPreview)
	}
}
