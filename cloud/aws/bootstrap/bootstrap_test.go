package bootstrap

import (
	"context"
	"slices"
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
			GlobalSecondaryIndexes []struct {
				IndexName string `yaml:"IndexName"`
				KeySchema []struct {
					AttributeName string `yaml:"AttributeName"`
					KeyType       string `yaml:"KeyType"`
				} `yaml:"KeySchema"`
				Projection struct {
					ProjectionType   string   `yaml:"ProjectionType"`
					NonKeyAttributes []string `yaml:"NonKeyAttributes"`
				} `yaml:"Projection"`
			} `yaml:"GlobalSecondaryIndexes"`
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

// TestStackTemplate_StateTable asserts both substrate templates provision the
// account-global state table with the generic single-table schema every Ocel
// state entity keys into: partition key pk (S), sort key sk (S), and a TTL on
// expires_at. The keys are deliberately opaque — upload sessions and Next ISR
// tag records share this table — so nothing here may name an entity.
func TestStackTemplate_StateTable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate()},
		{"preview", previewStackTemplate()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			table, ok := tmpl.Resources["StateTable"]
			if !ok {
				t.Fatal("template is missing the StateTable resource")
			}
			if table.Type != "AWS::DynamoDB::Table" {
				t.Errorf("StateTable Type = %q, want AWS::DynamoDB::Table", table.Type)
			}
			if table.Properties.BillingMode != "PAY_PER_REQUEST" {
				t.Errorf("BillingMode = %q, want PAY_PER_REQUEST", table.Properties.BillingMode)
			}

			attrs := table.Properties.AttributeDefinitions
			if len(attrs) != 4 ||
				attrs[0].AttributeName != "pk" || attrs[0].AttributeType != "S" ||
				attrs[1].AttributeName != "sk" || attrs[1].AttributeType != "S" ||
				attrs[2].AttributeName != "gsi1pk" || attrs[2].AttributeType != "S" ||
				attrs[3].AttributeName != "gsi1sk" || attrs[3].AttributeType != "S" {
				t.Errorf("AttributeDefinitions = %+v, want pk/sk and gsi1pk/gsi1sk, all (S)", attrs)
			}
			keys := table.Properties.KeySchema
			if len(keys) != 2 ||
				keys[0].AttributeName != "pk" || keys[0].KeyType != "HASH" ||
				keys[1].AttributeName != "sk" || keys[1].KeyType != "RANGE" {
				t.Errorf("KeySchema = %+v, want pk HASH + sk RANGE", keys)
			}

			// The index is what makes "which tags changed since I last looked"
			// a bounded query instead of a scan of an account-global table. It
			// is sparse by construction: only items that carry gsi1pk are
			// indexed, so upload sessions and the ISR handler's own tag items
			// stay out of it. The projection must carry every field the sync
			// reads, or a query that "worked" still costs a follow-up read per
			// tag.
			idxs := table.Properties.GlobalSecondaryIndexes
			if len(idxs) != 1 {
				t.Fatalf("GlobalSecondaryIndexes = %+v, want exactly the tag-sync index", idxs)
			}
			idx := idxs[0]
			if idx.IndexName != StateTableIndexName {
				t.Errorf("IndexName = %q, want %q", idx.IndexName, StateTableIndexName)
			}
			if len(idx.KeySchema) != 2 ||
				idx.KeySchema[0].AttributeName != "gsi1pk" || idx.KeySchema[0].KeyType != "HASH" ||
				idx.KeySchema[1].AttributeName != "gsi1sk" || idx.KeySchema[1].KeyType != "RANGE" {
				t.Errorf("index KeySchema = %+v, want gsi1pk HASH + gsi1sk RANGE", idx.KeySchema)
			}
			if idx.Projection.ProjectionType != "INCLUDE" {
				t.Errorf("index ProjectionType = %q, want INCLUDE", idx.Projection.ProjectionType)
			}
			if want := []string{"expired", "stale", "tag"}; !slices.Equal(idx.Projection.NonKeyAttributes, want) {
				t.Errorf("index NonKeyAttributes = %v, want %v", idx.Projection.NonKeyAttributes, want)
			}

			ttl := table.Properties.TimeToLiveSpecification
			if ttl.AttributeName != "expires_at" || !ttl.Enabled {
				t.Errorf("TimeToLiveSpecification = %+v, want expires_at enabled", ttl)
			}

			if _, ok := tmpl.Outputs[outputStateTable]; !ok {
				t.Errorf("template is missing the %s output", outputStateTable)
			}
			if _, ok := tmpl.Resources["SessionsTable"]; ok {
				t.Error("SessionsTable is superseded by StateTable and must not be provisioned")
			}
		})
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
// state table name, state bucket, version, and class marker that later
// deploys and the class guard depend on.
func TestCheckDeployed_ParsesOutputs(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputStateBucket), OutputValue: aws.String("bucket-123")},
				{OutputKey: aws.String(outputStateTable), OutputValue: aws.String("state-abc")},
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
	want := Deployed{Present: true, Version: 3, StateBucket: "bucket-123", StateTable: "state-abc", ArtifactBucket: "artifacts-xyz", AssetBucket: "assets-xyz", Class: ClassProduction}
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

// edgeUserTemplate is the subset of a rendered template needed to assert the
// edge reader IAM user and its inline ISR-read policy.
type edgeUserTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			UserName string `yaml:"UserName"`
			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect    string         `yaml:"Effect"`
						Action    any            `yaml:"Action"`
						Resource  string         `yaml:"Resource"`
						Condition map[string]any `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

// TestEdgeUser asserts both substrate templates provision the deterministic edge
// reader IAM user with an inline policy scoped to this stack's own stores: read
// on the asset bucket, BatchGetItem on the state table bounded to the TAG#
// partitions (so the edge key can never reach the upload-session HMAC secrets
// sharing the table), and the dormant lambda:Invoke* grant.
func TestEdgeUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		userName string
	}{
		{"production", stackTemplate(), EdgeUserName},
		{"preview", previewStackTemplate(), EdgePreviewUserName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tmpl edgeUserTemplate
			if err := yaml.Unmarshal([]byte(tc.template), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}
			user, ok := tmpl.Resources["EdgeUser"]
			if !ok {
				t.Fatal("template is missing the EdgeUser resource")
			}
			if user.Type != "AWS::IAM::User" {
				t.Errorf("EdgeUser Type = %q, want AWS::IAM::User", user.Type)
			}
			if user.Properties.UserName != tc.userName {
				t.Errorf("UserName = %q, want %q", user.Properties.UserName, tc.userName)
			}
			if len(user.Properties.Policies) != 1 {
				t.Fatalf("want exactly one inline policy, got %d", len(user.Properties.Policies))
			}

			stmts := user.Properties.Policies[0].PolicyDocument.Statement
			var s3, ddb, invoke bool
			for _, st := range stmts {
				if st.Resource == "${AssetBucket.Arn}/*" {
					s3 = st.Action == "s3:GetObject"
				}
				if cond, ok := st.Condition["ForAllValues:StringLike"].(map[string]any); ok {
					if keys, ok := cond["dynamodb:LeadingKeys"].([]any); ok && len(keys) == 1 && keys[0] == "TAG#*" {
						ddb = true
					}
				}
				if actions, ok := st.Action.([]any); ok {
					for _, a := range actions {
						if a == "lambda:InvokeFunctionUrl" {
							invoke = true
						}
					}
				}
			}
			if !s3 {
				t.Error("missing s3:GetObject on the asset bucket")
			}
			if !ddb {
				t.Error("missing dynamodb:BatchGetItem bounded to the TAG# LeadingKeys")
			}
			if !invoke {
				t.Error("missing the dormant lambda:Invoke* grant")
			}
		})
	}
}
