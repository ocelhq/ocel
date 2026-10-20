package account

import (
	"context"
	"encoding/json"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

type parsedTemplate struct {
	Resources map[string]struct {
		Type       string `json:"Type"`
		Properties struct {
			BillingMode          string `json:"BillingMode"`
			AttributeDefinitions []struct {
				AttributeName string `json:"AttributeName"`
				AttributeType string `json:"AttributeType"`
			} `json:"AttributeDefinitions"`
			KeySchema []struct {
				AttributeName string `json:"AttributeName"`
				KeyType       string `json:"KeyType"`
			} `json:"KeySchema"`
			GlobalSecondaryIndexes []struct {
				IndexName string `json:"IndexName"`
				KeySchema []struct {
					AttributeName string `json:"AttributeName"`
					KeyType       string `json:"KeyType"`
				} `json:"KeySchema"`
				Projection struct {
					ProjectionType   string   `json:"ProjectionType"`
					NonKeyAttributes []string `json:"NonKeyAttributes"`
				} `json:"Projection"`
			} `json:"GlobalSecondaryIndexes"`
			TimeToLiveSpecification struct {
				AttributeName string `json:"AttributeName"`
				Enabled       bool   `json:"Enabled"`
			} `json:"TimeToLiveSpecification"`
			StreamSpecification struct {
				StreamViewType string `json:"StreamViewType"`
			} `json:"StreamSpecification"`
			BucketName     string       `json:"BucketName"`
			AbacStatus     string       `json:"AbacStatus"`
			Bucket         templateText `json:"Bucket"`
			PolicyDocument struct {
				Version   string `json:"Version"`
				Statement []struct {
					Sid       string `json:"Sid"`
					Effect    string `json:"Effect"`
					Principal struct {
						Service string `json:"Service"`
					} `json:"Principal"`
					Action    any          `json:"Action"`
					Resource  templateText `json:"Resource"`
					Condition struct {
						StringEquals map[string]templateText `json:"StringEquals"`
					} `json:"Condition"`
				} `json:"Statement"`
			} `json:"PolicyDocument"`
			PublicAccessBlockConfiguration struct {
				BlockPublicAcls       bool `json:"BlockPublicAcls"`
				BlockPublicPolicy     bool `json:"BlockPublicPolicy"`
				IgnorePublicAcls      bool `json:"IgnorePublicAcls"`
				RestrictPublicBuckets bool `json:"RestrictPublicBuckets"`
			} `json:"PublicAccessBlockConfiguration"`
			LifecycleConfiguration struct {
				Rules []struct {
					Id                          string `json:"Id"`
					Status                      string `json:"Status"`
					ExpirationInDays            int    `json:"ExpirationInDays"`
					ExpiredObjectDeleteMarker   bool   `json:"ExpiredObjectDeleteMarker"`
					NoncurrentVersionExpiration struct {
						NoncurrentDays          int `json:"NoncurrentDays"`
						NewerNoncurrentVersions int `json:"NewerNoncurrentVersions"`
					} `json:"NoncurrentVersionExpiration"`
					AbortIncompleteMultipartUpload struct {
						DaysAfterInitiation int `json:"DaysAfterInitiation"`
					} `json:"AbortIncompleteMultipartUpload"`
				} `json:"Rules"`
			} `json:"LifecycleConfiguration"`
		} `json:"Properties"`
	} `json:"Resources"`
	Outputs map[string]struct {
		Value templateText `json:"Value"`
	} `json:"Outputs"`
}

func parseTemplateStr(t *testing.T, template string) parsedTemplate {
	t.Helper()
	var tmpl parsedTemplate
	if err := json.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid JSON: %v", err)
	}
	return tmpl
}

func TestStackTemplate(t *testing.T) {
	t.Run("state table", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
		}{
			{"production", coreBody(defaultNamespace, environment.TierProduction, "")},
			{"preview", coreBody(defaultNamespace, environment.TierPreview, "")},
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
}

func TestStateBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", coreBody(defaultNamespace, environment.TierProduction, "")},
		{"preview", coreBody(defaultNamespace, environment.TierPreview, "")},
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

func TestTheBootstrapKeepsABucketPerDataClassNamedForItsTierAndClass(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want map[string]string
	}{
		{environment.TierProduction, map[string]string{
			"StateBucket":    "ocel-prod-state-" + fixtureBucketTag,
			"ArtifactBucket": "ocel-prod-artifacts-" + fixtureBucketTag,
			"EdgeBucket":     "ocel-prod-edge-" + fixtureBucketTag,
			"CacheBucket":    "ocel-prod-cache-" + fixtureBucketTag,
		}},
		{environment.TierPreview, map[string]string{
			"StateBucket":    "ocel-preview-state-" + fixtureBucketTag,
			"ArtifactBucket": "ocel-preview-artifacts-" + fixtureBucketTag,
			"EdgeBucket":     "ocel-preview-edge-" + fixtureBucketTag,
			"CacheBucket":    "ocel-preview-cache-" + fixtureBucketTag,
		}},
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			tmpl := parseTemplateStr(t, coreBody(defaultNamespace, tc.tier, ""))

			got := map[string]string{}
			for id, resource := range tmpl.Resources {
				if resource.Type == "AWS::S3::Bucket" {
					got[id] = resource.Properties.BucketName
				}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("buckets = %v, want %v", got, tc.want)
			}
			for id := range tc.want {
				bucket := tmpl.Resources[id].Properties
				if bucket.AbacStatus != "Enabled" {
					t.Errorf("%s AbacStatus = %q, want Enabled", id, bucket.AbacStatus)
				}
				pab := bucket.PublicAccessBlockConfiguration
				if !pab.BlockPublicAcls || !pab.BlockPublicPolicy || !pab.IgnorePublicAcls || !pab.RestrictPublicBuckets {
					t.Errorf("%s PublicAccessBlockConfiguration = %+v, want all four blocks true", id, pab)
				}
			}
			if got := tmpl.Outputs[outputBucketTag].Value; got != fixtureBucketTag {
				t.Errorf("the %s output = %q, want the tag the buckets are named with, so the next bootstrap keeps them", outputBucketTag, got)
			}
		})
	}
}

func lifecycleOf(t *testing.T, tier environment.Tier, id string) (expires, abortsAfter int) {
	t.Helper()
	bucket, ok := parseTemplateStr(t, coreBody(defaultNamespace, tier, "")).Resources[id]
	if !ok {
		t.Fatalf("the core declares no %s", id)
	}
	rules := bucket.Properties.LifecycleConfiguration.Rules
	if len(rules) != 1 {
		t.Fatalf("%s lifecycle rules = %d, want exactly 1", id, len(rules))
	}
	if rules[0].Status != "Enabled" {
		t.Errorf("%s lifecycle rule Status = %q, want Enabled", id, rules[0].Status)
	}
	return rules[0].ExpirationInDays, rules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation
}

func TestEachBootstrapBucketAgesOutWhatItsDataClassOutlives(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			for _, tc := range []struct {
				id      string
				expires int
				why     string
			}{
				{"ArtifactBucket", 30, "a deployed function keeps its own copy of the code staged for it"},
				{"EdgeBucket", 0, "what an edge serves goes with the release that wrote it, never on a timer"},
				{"CacheBucket", CacheObjectLifetimeDays, "an entry no release rewrote in that long is one nothing reads"},
			} {
				expires, abortsAfter := lifecycleOf(t, tier, tc.id)
				if expires != tc.expires {
					t.Errorf("%s expires objects after %d days, want %d: %s", tc.id, expires, tc.expires, tc.why)
				}
				if abortsAfter != artifactAbortMultipartDays {
					t.Errorf("%s aborts incomplete uploads after %d days, want %d", tc.id, abortsAfter, artifactAbortMultipartDays)
				}
			}
		})
	}
}

func TestARebootstrapKeepsTheBucketsTheFirstOneNamed(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{})
	bucketNames := func() []string {
		var names []string
		for _, resource := range parseTemplateStr(t, stacks.template(coreStackName)).Resources {
			if resource.Type == "AWS::S3::Bucket" {
				names = append(names, resource.Properties.BucketName)
			}
		}
		slices.Sort(names)
		return names
	}

	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first := bucketNames()
	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("Run again: %v", err)
	}

	if len(first) != 4 {
		t.Fatalf("the first bootstrap named buckets %v, want four", first)
	}
	for _, name := range first {
		tag := name[strings.LastIndex(name, "-")+1:]
		if !naming.IsRandomTag(tag) {
			t.Errorf("bucket %s ends in %q, want a random tag minted for it", name, tag)
		}
	}
	if again := bucketNames(); !slices.Equal(again, first) {
		t.Errorf("the second bootstrap names buckets %v, want the first one's %v: a new name replaces the bucket and strands what it holds", again, first)
	}
}

type stubStack struct {
	outputs []cfntypes.Output
	tags    []cfntypes.Tag
}

func (s stubStack) stamped(stamp Stamp) stubStack {
	s.tags = stampTags(defaultNamespace, environment.TierProduction, stamp)
	return s
}

type stubStacksAPI map[string]stubStack

func (s stubStacksAPI) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	stack, ok := s[aws.ToString(in.StackName)]
	if !ok {
		return nil, validationError{msg: "Stack with id " + aws.ToString(in.StackName) + " does not exist"}
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{Outputs: stack.outputs, Tags: stack.tags}}}, nil
}

func outputs(pairs map[string]string) stubStack {
	var out stubStack
	for k, v := range pairs {
		out.outputs = append(out.outputs, cfntypes.Output{OutputKey: aws.String(k), OutputValue: aws.String(v)})
	}
	return out
}

func TestCheckDeployed(t *testing.T) {
	t.Run("parses outputs", func(t *testing.T) {
		core := map[string]string{
			outputStateBucket:    "bucket-123",
			outputStateTable:     "state-abc",
			outputArtifactBucket: "artifacts-xyz",
			outputEdgeBucket:     "edge-xyz",
			outputCacheBucket:    "cache-xyz",
			outputBucketTag:      fixtureBucketTag,
			outputInfraTier:      string(environment.TierProduction),
		}
		api := stubStacksAPI{coreStackName: outputs(core).stamped(Stamp{Digest: "written-digest", WrittenBy: "1.4.0"})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		want := Deployed{
			Present:        true,
			Parts:          PartSet{},
			StateBucket:    "bucket-123",
			StateTable:     "state-abc",
			ArtifactBucket: "artifacts-xyz",
			EdgeBucket:     "edge-xyz",
			CacheBucket:    "cache-xyz",
			Tier:           environment.TierProduction,
			Outputs:        core,
			Stacks: []StackStamp{
				{
					Name:      coreStackName,
					Present:   true,
					Digest:    "written-digest",
					Intended:  cfn.TemplateDigest(coreBody(defaultNamespace, environment.TierProduction, "")),
					WrittenBy: "1.4.0",
				},
				{Name: coreStackName + "-" + CloudflareEdgePart, Part: CloudflareEdgePart},
				{Name: coreStackName + "-" + provider.VariablesKeyPart, Part: provider.VariablesKeyPart},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("CheckDeployed = %+v, want %+v", got, want)
		}
	})

	t.Run("reads preview tier marker", func(t *testing.T) {
		api := stubStacksAPI{coreStackName: outputs(map[string]string{outputInfraTier: string(environment.TierPreview)})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if !got.Present || got.Tier != environment.TierPreview {
			t.Errorf("CheckDeployed = %+v, want Present with Tier %q", got, environment.TierPreview)
		}
	})

	t.Run("a part stack is what says the part is on", func(t *testing.T) {
		api := stubStacksAPI{
			coreStackName:                            outputs(map[string]string{outputInfraTier: string(environment.TierProduction)}).stamped(Stamp{}),
			coreStackName + "-" + CloudflareEdgePart: outputs(nil).stamped(Stamp{}),
		}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if want := []string{CloudflareEdgePart}; !reflect.DeepEqual(got.Parts.Names(), want) {
			t.Errorf("Parts = %v, want %v", got.Parts.Names(), want)
		}
	})

	t.Run("the base roles the parts run as reach the flat fields a deploy reads", func(t *testing.T) {
		api := stubStacksAPI{coreStackName: outputs(map[string]string{
			outputInfraTier:             string(environment.TierProduction),
			outputTagInvalidatorRoleARN: "arn:aws:iam::111122223333:role/ocel/production/bootstrap/ocel-invalidator",
			OutputEdgeInvokeRoleARN:     "arn:aws:iam::111122223333:role/ocel-edge-invoke",
			outputStateTableStreamARN:   "arn:aws:dynamodb:us-east-1:111122223333:table/state/stream/1",
		}).stamped(Stamp{})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if got.TagInvalidatorRoleARN == "" || got.EdgeInvokeRoleARN == "" || got.StateTableStreamARN == "" {
			t.Errorf("CheckDeployed = %+v, want the invalidator role, the invoke role and the state table stream read off the core", got)
		}
	})

	t.Run("a stack whose digest moved reads as stale", func(t *testing.T) {
		api := stubStacksAPI{coreStackName: outputs(map[string]string{outputInfraTier: string(environment.TierProduction)}).
			stamped(Stamp{Digest: "stale"})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if stale := got.Stale(nil); len(stale) != 1 || stale[0].Name != coreStackName {
			t.Errorf("Stale() = %+v, want the core stack alone: a part that was never installed is not stale", stale)
		}
	})

	t.Run("a stack written from this build reads as current", func(t *testing.T) {
		api := stubStacksAPI{coreStackName: outputs(map[string]string{outputInfraTier: string(environment.TierProduction), outputBucketTag: fixtureBucketTag}).
			stamped(Stamp{Digest: cfn.TemplateDigest(coreBody(defaultNamespace, environment.TierProduction, ""))})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if stale := got.Stale(nil); len(stale) != 0 {
			t.Errorf("Stale() = %+v, want nothing", stale)
		}
	})
}

func TestPreviewStackTemplate(t *testing.T) {
	t.Run("stamps preview tier", func(t *testing.T) {
		var tmpl parsedTemplate
		if err := json.Unmarshal([]byte(coreBody(defaultNamespace, environment.TierPreview, "")), &tmpl); err != nil {
			t.Fatalf("preview template is not valid JSON: %v", err)
		}
		if got := tmpl.Outputs[outputInfraTier].Value; got != templateText(environment.TierPreview) {
			t.Errorf("%s output = %q, want %q", outputInfraTier, got, environment.TierPreview)
		}
	})
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

func listPolicyActions(action any) []string {
	switch a := action.(type) {
	case string:
		return []string{a}
	case []any:
		var actions []string
		for _, v := range a {
			if name, ok := v.(string); ok {
				actions = append(actions, name)
			}
		}
		return actions
	}
	return nil
}

const edgeTagKeyPattern = "PROJECT#*#TAG#*"

func TestEdgeTagKeys(t *testing.T) {
	release, err := naming.ParseReleaseToken("r3f8a1c9d")
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
			"PROJECT#shop",
			"PROJECT#shop#CLASS#production",
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
		t.Fatalf("pattern %q or value %q contains a character path.Match reads differently from IAM StringLike", pattern, value)
	}
	ok, err := path.Match(pattern, value)
	if err != nil {
		t.Fatalf("path.Match(%q, %q): %v", pattern, value, err)
	}
	return ok
}

func TestOnlyTheEdgeBucketAdmitsCloudFrontAndOnlyToItsAssets(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			tmpl := parseTemplateStr(t, coreBody(defaultNamespace, tier, ""))

			var policies []string
			for id, resource := range tmpl.Resources {
				if resource.Type == "AWS::S3::BucketPolicy" {
					policies = append(policies, id)
				}
			}
			if !slices.Equal(policies, []string{"EdgeBucketPolicy"}) {
				t.Fatalf("bucket policies = %v, want only EdgeBucketPolicy: CloudFront's read is account-wide, so it may reach only what an edge serves", policies)
			}
			policy := tmpl.Resources["EdgeBucketPolicy"].Properties
			if got := policy.Bucket; got != "EdgeBucket" {
				t.Errorf("the policy is attached to %q, want the edge bucket", got)
			}
			statements := policy.PolicyDocument.Statement
			if len(statements) != 1 {
				t.Fatalf("the policy has %d statements, want exactly one: it is written once by the bootstrap and never rewritten per deploy", len(statements))
			}
			grant := statements[0]
			if grant.Effect != "Allow" || !slices.Equal(templateStrings(grant.Action), []string{"s3:GetObject"}) {
				t.Errorf("the grant is %s %v, want Allow s3:GetObject", grant.Effect, grant.Action)
			}
			if grant.Principal.Service != "cloudfront.amazonaws.com" {
				t.Errorf("principal = %q, want the CloudFront service principal and nothing wider", grant.Principal.Service)
			}
			if got := grant.Resource; got != "${EdgeBucket.Arn}/assets/*" {
				t.Errorf("resource = %q, want the edge bucket's assets and nothing else: CloudFront serves only a release's static assets out of it", got)
			}
			if got := grant.Condition.StringEquals["AWS:SourceAccount"]; got != "AWS::AccountId" {
				t.Errorf("source account condition = %q, want this account, so no other account's distribution can read the bucket", got)
			}
		})
	}
}

func TestStackWaitersPollFarSoonerThanTheSDKDefault(t *testing.T) {
	create := cloudformation.StackCreateCompleteWaiterOptions{}
	cfn.CreateCadence(&create)
	update := cloudformation.StackUpdateCompleteWaiterOptions{}
	cfn.UpdateCadence(&update)
	removal := cloudformation.StackDeleteCompleteWaiterOptions{}
	cfn.DeleteCadence(&removal)

	cadences := map[string][2]time.Duration{
		"create": {create.MinDelay, create.MaxDelay},
		"update": {update.MinDelay, update.MaxDelay},
		"delete": {removal.MinDelay, removal.MaxDelay},
	}
	for name, got := range cadences {
		if got[0] != 5*time.Second {
			t.Errorf("the %s waiter waits %s between looks, want 5s: a stack that is already done should not idle", name, got[0])
		}
		if got[1] != 20*time.Second {
			t.Errorf("the %s waiter backs off to %s, want 20s", name, got[1])
		}
	}
}
