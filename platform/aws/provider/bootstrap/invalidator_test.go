package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func fixtureInvalidatorCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(tagInvalidatorKeyPrefix, payloads.TagInvalidator().SHA256)}
}

var invalidatorResourceNames = []string{
	"TagInvalidator", "TagInvalidatorStream", "TagInvalidatorRole",
	"TagInvalidatorDeadLetterQueue",
}

func TestTagInvalidator(t *testing.T) {
	t.Run("consumes the stream into a dead letter queue", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
		}{
			{"production", featureTemplate(FeatureISR, ClassProduction)},
			{"preview", featureTemplate(FeatureISR, ClassPreview)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parsePublisherTemplate(t, tc.template)

				fn, ok := tmpl.Resources["TagInvalidator"]
				if !ok {
					t.Fatal("template is missing the TagInvalidator function")
				}
				if fn.Type != "AWS::Lambda::Function" {
					t.Errorf("TagInvalidator Type = %q, want AWS::Lambda::Function", fn.Type)
				}
				if fn.Properties.Code.S3Bucket != fixtureInvalidatorCode().Bucket ||
					fn.Properties.Code.S3Key != fixtureInvalidatorCode().Key {
					t.Errorf("TagInvalidator Code = %+v, want the placed payload", fn.Properties.Code)
				}

				esm, ok := tmpl.Resources["TagInvalidatorStream"]
				if !ok {
					t.Fatal("template is missing the TagInvalidatorStream event source mapping")
				}
				if esm.Type != "AWS::Lambda::EventSourceMapping" {
					t.Errorf("TagInvalidatorStream Type = %q, want AWS::Lambda::EventSourceMapping", esm.Type)
				}
				if esm.Properties.EventSourceArn != paramStateTableStreamARN {
					t.Errorf("EventSourceArn = %q, want the state table's stream, which is where a raise lands", esm.Properties.EventSourceArn)
				}
				if got := esm.Properties.FilterCriteria.Filters; len(got) != 1 || got[0].Pattern != tagRecordStreamFilter {
					t.Errorf("FilterCriteria.Filters = %+v, want the tag record filter; without it every write in the bootstrap wakes this function", got)
				}
				if w := esm.Properties.MaximumBatchingWindowInSeconds; w == nil || *w != 0 {
					t.Errorf("MaximumBatchingWindowInSeconds = %v, want 0 — an invalidation must not wait on a batch filling", w)
				}
				if want := []string{"ReportBatchItemFailures"}; !slices.Equal(esm.Properties.FunctionResponseTypes, want) {
					t.Errorf("FunctionResponseTypes = %v, want %v — one poison build must not fail its batch-mates", esm.Properties.FunctionResponseTypes, want)
				}
				if r := esm.Properties.MaximumRetryAttempts; r == nil || *r != tagInvalidatorRetries {
					t.Errorf("MaximumRetryAttempts = %v, want %d — unbounded retries stall the shard", r, tagInvalidatorRetries)
				}
				if got := esm.Properties.DestinationConfig.OnFailure.Destination; got != "TagInvalidatorDeadLetterQueue.Arn" {
					t.Errorf("OnFailure destination = %q, want the invalidator's own dead-letter queue", got)
				}

				dlq, ok := tmpl.Resources["TagInvalidatorDeadLetterQueue"]
				if !ok {
					t.Fatal("template is missing the TagInvalidatorDeadLetterQueue")
				}
				if dlq.Type != "AWS::SQS::Queue" {
					t.Errorf("TagInvalidatorDeadLetterQueue Type = %q, want AWS::SQS::Queue", dlq.Type)
				}
				if !dlq.Properties.SqsManagedSseEnabled {
					t.Error("the dead-letter queue is unencrypted")
				}
			})
		}
	})

	t.Run("reads the ledger of its own class", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
			class    string
		}{
			{"production", featureTemplate(FeatureISR, ClassProduction), ClassProduction},
			{"preview", featureTemplate(FeatureISR, ClassPreview), ClassPreview},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := parsePublisherTemplate(t, tc.template).Resources["TagInvalidator"].Properties.Environment.Variables
				if env[tagInvalidatorClassEnvVar] != tc.class {
					t.Errorf("%s = %q, want %q — the class scopes every ledger read to its own bootstrap", tagInvalidatorClassEnvVar, env[tagInvalidatorClassEnvVar], tc.class)
				}
				if env[tagInvalidatorStateTableEnvVar] != paramStateTableName {
					t.Errorf("%s = %q, want the bootstrap's state table", tagInvalidatorStateTableEnvVar, env[tagInvalidatorStateTableEnvVar])
				}
			})
		}
	})

	t.Run("role reaches only the stream, the ledger and invalidation", func(t *testing.T) {
		tmpl := parsePublisherTemplate(t, featureTemplate(FeatureISR, ClassProduction))
		role, ok := tmpl.Resources["TagInvalidatorRole"]
		if !ok {
			t.Fatal("template is missing the TagInvalidatorRole")
		}
		if len(role.Properties.Policies) != 1 {
			t.Fatalf("role policies = %+v, want exactly one inline policy", role.Properties.Policies)
		}

		var actions []string
		for _, st := range role.Properties.Policies[0].PolicyDocument.Statement {
			switch a := st.Action.(type) {
			case string:
				actions = append(actions, a)
			case []any:
				for _, one := range a {
					actions = append(actions, one.(string))
				}
			}
			if a, ok := st.Action.(string); ok && a == "cloudfront:CreateInvalidation" {
				if resource, ok := st.Resource.(string); !ok || !strings.Contains(resource, "${AWS::AccountId}") {
					t.Errorf("CreateInvalidation resource = %v, want this account's distributions rather than every account's", st.Resource)
				}
			}
		}
		for _, want := range []string{"dynamodb:GetItem", "cloudfront:CreateInvalidation"} {
			if !slices.Contains(actions, want) {
				t.Errorf("role grants %v, missing %s", actions, want)
			}
		}
		for _, forbidden := range []string{
			"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:Query",
			"cloudfront:UpdateDistribution", "cloudfront:CreateDistribution", "s3:GetObject",
		} {
			if slices.Contains(actions, forbidden) {
				t.Errorf("role grants %s; the invalidator reads the stream and the ledger, and invalidates, nothing else", forbidden)
			}
		}
	})

	t.Run("renders no alarms", func(t *testing.T) {
		for name, res := range parsePublisherTemplate(t, featureTemplate(FeatureISR, ClassProduction)).Resources {
			if res.Type == "AWS::CloudWatch::Alarm" {
				t.Errorf("%s is a billed standing alarm in a stack that must be free to leave idle", name)
			}
		}
	})

	t.Run("every bootstrap that carries isr carries one", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			target    spec
			stackName string
		}{
			{"production", productionBootstrap(), isrStack(ClassProduction)},
			{"preview", previewBootstrap(), isrStack(ClassPreview)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfn := newFakeCFN()
				frontedBy(t, &fakeEdge{kind: "cloudflare"})

				if err := runAll(context.Background(), apisOf(cfn, newFakeSSM(), &fakeIAM{}, preloadedStore()), tc.target); err != nil {
					t.Fatalf("run: %v", err)
				}
				for _, name := range invalidatorResourceNames {
					if !strings.Contains(cfn.template(tc.stackName), name+":") {
						t.Errorf("%s declares no %s, so no front ever hears a raise", tc.stackName, name)
					}
				}
			})
		}
	})
}
