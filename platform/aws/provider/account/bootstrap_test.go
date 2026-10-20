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
					Prefix                      string `json:"Prefix"`
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

type stubStack struct {
	outputs []cfntypes.Output
	tags    []cfntypes.Tag
	status  cfntypes.StackStatus
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
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{Outputs: stack.outputs, Tags: stack.tags, StackStatus: stack.status}}}, nil
}

func outputs(pairs map[string]string) stubStack {
	var out stubStack
	for k, v := range pairs {
		out.outputs = append(out.outputs, cfntypes.Output{OutputKey: aws.String(k), OutputValue: aws.String(v)})
	}
	return out
}

func coreOutputs(pairs map[string]string) stubStack {
	all := fixtureBucketOutputs(environment.TierProduction)
	maps.Copy(all, pairs)
	return outputs(all)
}

func TestCheckDeployed(t *testing.T) {
	t.Run("parses outputs", func(t *testing.T) {
		core := map[string]string{
			outputStateBucket:    "ocel-prod-state-" + fixtureBucketTag,
			outputStateTable:     "state-abc",
			outputArtifactBucket: "ocel-prod-artifacts-" + fixtureBucketTag,
			outputEdgeBucket:     "ocel-prod-edge-" + fixtureBucketTag,
			outputCacheBucket:    "ocel-prod-cache-" + fixtureBucketTag,
			outputInfraTier:      string(environment.TierProduction),
		}
		api := stubStacksAPI{coreStackName: outputs(core).stamped(Stamp{Digest: "written-digest", WrittenBy: "1.4.0"})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		want := Deployed{
			Present: true,
			Parts:   PartSet{},
			Buckets: naming.Buckets{
				naming.StorageState:     "ocel-prod-state-" + fixtureBucketTag,
				naming.StorageArtifacts: "ocel-prod-artifacts-" + fixtureBucketTag,
				naming.StorageEdge:      "ocel-prod-edge-" + fixtureBucketTag,
				naming.StorageCache:     "ocel-prod-cache-" + fixtureBucketTag,
			},
			StateTable: "state-abc",
			Tier:       environment.TierProduction,
			Outputs:    core,
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
		api := stubStacksAPI{coreStackName: coreOutputs(map[string]string{outputInfraTier: string(environment.TierPreview)})}

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
			coreStackName:                            coreOutputs(map[string]string{outputInfraTier: string(environment.TierProduction)}).stamped(Stamp{}),
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
		api := stubStacksAPI{coreStackName: coreOutputs(map[string]string{
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
		api := stubStacksAPI{coreStackName: coreOutputs(map[string]string{outputInfraTier: string(environment.TierProduction)}).
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
		api := stubStacksAPI{coreStackName: outputs(map[string]string{
			outputInfraTier:      string(environment.TierProduction),
			outputStateBucket:    "ocel-prod-state-" + fixtureBucketTag,
			outputArtifactBucket: "ocel-prod-artifacts-" + fixtureBucketTag,
			outputEdgeBucket:     "ocel-prod-edge-" + fixtureBucketTag,
			outputCacheBucket:    "ocel-prod-cache-" + fixtureBucketTag,
		}).
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

func TestABootstrapWhoseCoreNamesNoBucketYetReadsAsNotYetBootstrappedRatherThanOneToDelete(t *testing.T) {
	for _, status := range []cfntypes.StackStatus{cfntypes.StackStatusCreateInProgress, cfntypes.StackStatusReviewInProgress} {
		t.Run(string(status), func(t *testing.T) {
			api := stubStacksAPI{coreStackName: stubStack{status: status}.stamped(Stamp{})}

			read, err := Read(context.Background(), api, defaultNamespace, environment.TierProduction)
			if err != nil {
				t.Fatalf("Read = %v, want no error: a core still being created has no outputs yet, and deleting it is never the answer", err)
			}
			if read.Deployed.Present {
				t.Errorf("Read = %+v, want a bootstrap not yet present", read.Deployed)
			}
		})
	}
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
