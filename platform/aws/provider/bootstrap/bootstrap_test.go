package bootstrap

import (
	"context"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

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
			StreamSpecification struct {
				StreamViewType string `yaml:"StreamViewType"`
			} `yaml:"StreamSpecification"`
			Bucket         string `yaml:"Bucket"`
			PolicyDocument struct {
				Version   string `yaml:"Version"`
				Statement []struct {
					Sid       string `yaml:"Sid"`
					Effect    string `yaml:"Effect"`
					Principal struct {
						Service string `yaml:"Service"`
					} `yaml:"Principal"`
					Action    string `yaml:"Action"`
					Resource  string `yaml:"Resource"`
					Condition struct {
						StringEquals map[string]string `yaml:"StringEquals"`
					} `yaml:"Condition"`
				} `yaml:"Statement"`
			} `yaml:"PolicyDocument"`
			PublicAccessBlockConfiguration struct {
				BlockPublicAcls       bool `yaml:"BlockPublicAcls"`
				BlockPublicPolicy     bool `yaml:"BlockPublicPolicy"`
				IgnorePublicAcls      bool `yaml:"IgnorePublicAcls"`
				RestrictPublicBuckets bool `yaml:"RestrictPublicBuckets"`
			} `yaml:"PublicAccessBlockConfiguration"`
			LifecycleConfiguration struct {
				Rules []struct {
					Id                          string `yaml:"Id"`
					Status                      string `yaml:"Status"`
					ExpirationInDays            int    `yaml:"ExpirationInDays"`
					ExpiredObjectDeleteMarker   bool   `yaml:"ExpiredObjectDeleteMarker"`
					NoncurrentVersionExpiration struct {
						NoncurrentDays          int `yaml:"NoncurrentDays"`
						NewerNoncurrentVersions int `yaml:"NewerNoncurrentVersions"`
					} `yaml:"NoncurrentVersionExpiration"`
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
	return parseTemplateStr(t, stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion))
}

func parseTemplateStr(t *testing.T, template string) parsedTemplate {
	t.Helper()
	var tmpl parsedTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func TestStackTemplate(t *testing.T) {
	t.Run("state table", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
		}{
			{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
			{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
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

				if view := table.Properties.StreamSpecification.StreamViewType; view != "NEW_IMAGE" {
					t.Errorf("StreamViewType = %q, want NEW_IMAGE", view)
				}

				if _, ok := tmpl.Outputs[outputStateTable]; !ok {
					t.Errorf("template is missing the %s output", outputStateTable)
				}
				if _, ok := tmpl.Resources["SessionsTable"]; ok {
					t.Error("SessionsTable is superseded by StateTable and must not be provisioned")
				}
			})
		}
	})

	t.Run("version output", func(t *testing.T) {
		tmpl := parseTemplate(t)
		if got := tmpl.Outputs[outputVersion].Value; got != strconv.Itoa(RequiredBootstrapVersion) {
			t.Errorf("%s output = %q, want %d", outputVersion, got, RequiredBootstrapVersion)
		}
	})
}

func TestStateBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			bucket, ok := tmpl.Resources["StateBucket"]
			if !ok {
				t.Fatal("template is missing the StateBucket resource")
			}

			rules := bucket.Properties.LifecycleConfiguration.Rules
			if len(rules) != 2 {
				t.Fatalf("StateBucket lifecycle rules = %d, want exactly 2", len(rules))
			}

			byID := map[string]int{}
			for i, rule := range rules {
				byID[rule.Id] = i
				if rule.Status != "Enabled" {
					t.Errorf("rule %q Status = %q, want Enabled", rule.Id, rule.Status)
				}
				if rule.ExpirationInDays != 0 {
					t.Errorf("rule %q ExpirationInDays = %d, want 0: state checkpoints are current versions and must not expire", rule.Id, rule.ExpirationInDays)
				}
			}

			expire, ok := byID["expire-noncurrent-state"]
			if !ok {
				t.Fatalf("lifecycle rules %v are missing expire-noncurrent-state", rules)
			}
			if got := rules[expire].NoncurrentVersionExpiration.NoncurrentDays; got != stateNoncurrentDays {
				t.Errorf("NoncurrentDays = %d, want %d", got, stateNoncurrentDays)
			}
			if got := rules[expire].NoncurrentVersionExpiration.NewerNoncurrentVersions; got != 0 {
				t.Errorf("NewerNoncurrentVersions = %d, want 0: retained versions survive a teardown forever", got)
			}
			if got := rules[expire].AbortIncompleteMultipartUpload.DaysAfterInitiation; got != stateAbortMultipartDays {
				t.Errorf("AbortIncompleteMultipartUpload = %d, want %d", got, stateAbortMultipartDays)
			}

			sweep, ok := byID["expire-state-delete-markers"]
			if !ok {
				t.Fatalf("lifecycle rules %v are missing expire-state-delete-markers", rules)
			}
			if !rules[sweep].ExpiredObjectDeleteMarker {
				t.Error("ExpiredObjectDeleteMarker = false, want true: a delete marker left behind is an object left behind")
			}
		})
	}
}

func TestArtifactBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
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

func TestAssetBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
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

type stubDescriber struct {
	out *cloudformation.DescribeStacksOutput
}

func (s stubDescriber) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return s.out, nil
}

func TestCheckDeployed(t *testing.T) {
	t.Run("parses outputs", func(t *testing.T) {
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
	})

	t.Run("reads preview class marker", func(t *testing.T) {
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
	})
}

func TestPreviewStackTemplate(t *testing.T) {
	t.Run("stamps preview class", func(t *testing.T) {
		var tmpl parsedTemplate
		if err := yaml.Unmarshal([]byte(previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)), &tmpl); err != nil {
			t.Fatalf("preview template is not valid YAML: %v", err)
		}
		if got := tmpl.Outputs[outputInfraClass].Value; got != ClassPreview {
			t.Errorf("%s output = %q, want %q", outputInfraClass, got, ClassPreview)
		}
	})
}

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
						Resource  any            `yaml:"Resource"`
						Condition map[string]any `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func TestEdgeUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		userName string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion), EdgeUserName},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion), EdgePreviewUserName},
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

			if name := user.Properties.Policies[0].PolicyName; name != "ocel-edge-cache" {
				t.Errorf("PolicyName = %q, want ocel-edge-cache", name)
			}

			stmts := user.Properties.Policies[0].PolicyDocument.Statement
			var s3Read, s3Write, ddbTable, ddbIndex, invoke, invokeTagged bool
			for _, st := range stmts {
				if st.Resource == "${AssetBucket.Arn}/*" {
					s3Read = hasAction(st.Action, "s3:GetObject")
					if hasAction(st.Action, "s3:PutObject") {
						t.Error("s3:PutObject must not be granted bucket-wide")
					}
				}
				if st.Resource == "${AssetBucket.Arn}/*/fetch-cache/*.cache.json" {
					s3Write = hasAction(st.Action, "s3:PutObject")
				}
				if st.Resource == "StateTable.Arn" && boundToTagKeys(st.Condition) {
					ddbTable = hasAction(st.Action, "dynamodb:BatchGetItem") && hasAction(st.Action, "dynamodb:UpdateItem")
				}
				if st.Resource == "${StateTable.Arn}/index/"+StateTableIndexName && boundToTagKeys(st.Condition) {
					ddbIndex = hasAction(st.Action, "dynamodb:Query")
				}
				if hasAction(st.Action, "lambda:InvokeFunctionUrl") {
					invoke = true
					if equals, ok := st.Condition["StringEquals"].(map[string]any); ok {
						if equals["aws:ResourceTag/ocel:component"] == "function" {
							invokeTagged = true
						}
					}
				}
			}
			if !s3Read {
				t.Error("missing s3:GetObject on the asset bucket")
			}
			if !s3Write {
				t.Error("missing s3:PutObject scoped to a .cache.json object under the fetch-cache prefix")
			}
			if !ddbTable {
				t.Error("missing dynamodb:BatchGetItem + UpdateItem bounded to the TAG# LeadingKeys")
			}
			if !ddbIndex {
				t.Error("missing dynamodb:Query on the table's index bounded to the TAG# LeadingKeys")
			}
			if !invoke {
				t.Error("missing the lambda:Invoke* grant")
			}
			if !invokeTagged {
				t.Error("lambda:Invoke* grant must be gated on ocel:component being function, so it reaches no listener or other Ocel-run function")
			}
		})
	}
}

func hasAction(action any, want string) bool {
	switch a := action.(type) {
	case string:
		return a == want
	case []any:
		for _, v := range a {
			if v == want {
				return true
			}
		}
	}
	return false
}

const edgeTagKeyPattern = "PROJECT#*#TAG#*"

func boundToTagKeys(condition map[string]any) bool {
	cond, ok := condition["ForAllValues:StringLike"].(map[string]any)
	if !ok {
		return false
	}
	keys, ok := cond["dynamodb:LeadingKeys"].([]any)
	return ok && len(keys) == 1 && keys[0] == edgeTagKeyPattern
}

func TestEdgeTagKeys(t *testing.T) {
	release, err := naming.ParseRelease("r3f8a1c9d")
	if err != nil {
		t.Fatalf("ParseRelease: %v", err)
	}
	stack := naming.AppStack("prod", "web", release)

	t.Run("admits every tag partition the edge writes", func(t *testing.T) {
		for _, key := range []string{
			naming.StackKey("shop", stack) + "#TAG#products",
			naming.StackKey("shop", naming.AppStack("pr-7", "admin", release)) + "#TAG#a#b",
		} {
			if !stringLike(t, edgeTagKeyPattern, key) {
				t.Errorf("%q denies the tag key %q", edgeTagKeyPattern, key)
			}
		}
	})

	t.Run("admits no other partition in the table", func(t *testing.T) {
		for _, key := range []string{
			"PROJECTS",
			naming.ProjectKey("shop"),
			naming.VarsKey("shop", "production"),
			naming.StackKey("shop", stack),
			naming.SessionKeyPrefix("shop", "prod") + "01hxyz",
		} {
			if stringLike(t, edgeTagKeyPattern, key) {
				t.Errorf("%q admits %q, which is not a tag partition", edgeTagKeyPattern, key)
			}
		}
	})
}

func stringLike(t *testing.T, pattern, value string) bool {
	t.Helper()
	if strings.ContainsAny(pattern+value, "?[]/\\") {
		t.Fatalf("pattern %q or value %q carries a character path.Match reads differently from IAM StringLike", pattern, value)
	}
	ok, err := path.Match(pattern, value)
	if err != nil {
		t.Fatalf("path.Match(%q, %q): %v", pattern, value, err)
	}
	return ok
}

func TestAssetBucketGrantsCloudFrontRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			policy, held := tmpl.Resources["AssetBucketPolicy"]
			if !held {
				t.Fatalf("the template holds %v, want a bucket policy: nothing else lets a CloudFront distribution read a static asset out of the bucket", slices.Sorted(maps.Keys(tmpl.Resources)))
			}
			if policy.Type != "AWS::S3::BucketPolicy" {
				t.Errorf("AssetBucketPolicy type = %q, want AWS::S3::BucketPolicy", policy.Type)
			}
			if got := policy.Properties.Bucket; got != "AssetBucket" {
				t.Errorf("the policy is attached to %q, want the asset bucket", got)
			}
			statements := policy.Properties.PolicyDocument.Statement
			if len(statements) != 1 {
				t.Fatalf("the policy holds %d statements, want exactly one: it is written once by the bootstrap and never rewritten per deploy", len(statements))
			}
			grant := statements[0]
			if grant.Effect != "Allow" || grant.Action != "s3:GetObject" {
				t.Errorf("the grant is %s %s, want Allow s3:GetObject", grant.Effect, grant.Action)
			}
			if grant.Principal.Service != "cloudfront.amazonaws.com" {
				t.Errorf("principal = %q, want the CloudFront service principal and nothing wider", grant.Principal.Service)
			}
			if got := grant.Resource; got != "${AssetBucket.Arn}/*" {
				t.Errorf("resource = %q, want every object in the asset bucket and nothing outside it", got)
			}
			if got := grant.Condition.StringEquals["AWS:SourceAccount"]; got != "AWS::AccountId" {
				t.Errorf("source account condition = %q, want this account, so no other account's distribution can read the bucket", got)
			}
		})
	}
}
