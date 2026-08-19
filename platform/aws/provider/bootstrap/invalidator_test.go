package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func fixtureInvalidatorCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(tagInvalidatorKeyPrefix, payloads.TagInvalidator().SHA256)}
}

func invalidatingPayloads() stackPayloads {
	code := fixturePayloads()
	code.invalidator = fixtureInvalidatorCode()
	return code
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
			{"production", stackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion)},
			{"preview", previewStackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion)},
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
				if esm.Properties.EventSourceArn != "StateTable.StreamArn" {
					t.Errorf("EventSourceArn = %q, want the state table's stream, which is where a raise lands", esm.Properties.EventSourceArn)
				}
				if got := esm.Properties.FilterCriteria.Filters; len(got) != 1 || got[0].Pattern != tagRecordStreamFilter {
					t.Errorf("FilterCriteria.Filters = %+v, want the tag record filter; without it every write in the substrate wakes this function", got)
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

	t.Run("reads the ledger of its own substrate class", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
			class    string
		}{
			{"production", stackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion), ClassProduction},
			{"preview", previewStackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion), ClassPreview},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := parsePublisherTemplate(t, tc.template).Resources["TagInvalidator"].Properties.Environment.Variables
				if env[tagInvalidatorClassEnvVar] != tc.class {
					t.Errorf("%s = %q, want %q — the class scopes every ledger read to its own substrate", tagInvalidatorClassEnvVar, env[tagInvalidatorClassEnvVar], tc.class)
				}
				if env[tagInvalidatorStateTableEnvVar] != "StateTable" {
					t.Errorf("%s = %q, want the substrate's state table", tagInvalidatorStateTableEnvVar, env[tagInvalidatorStateTableEnvVar])
				}
			})
		}
	})

	t.Run("role reaches only the stream, the ledger and invalidation", func(t *testing.T) {
		tmpl := parsePublisherTemplate(t, stackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion))
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
		for name, res := range parsePublisherTemplate(t, stackTemplate(edge.TrustInternal, invalidatingPayloads(), RequiredBootstrapVersion)).Resources {
			if res.Type == "AWS::CloudWatch::Alarm" {
				t.Errorf("%s is a billed standing alarm in a stack that must be free to leave idle", name)
			}
		}
	})

	t.Run("no invalidator payload renders nothing", func(t *testing.T) {
		tmpl := stackTemplate(edge.TrustInternal, fixturePayloads(), RequiredBootstrapVersion)
		for _, name := range invalidatorResourceNames {
			if strings.Contains(tmpl, name+":") {
				t.Errorf("a build with no invalidator payload still rendered %s", name)
			}
		}
		if _, err := yaml.Marshal(parsePublisherTemplate(t, tmpl)); err != nil {
			t.Fatalf("template without an invalidator is not valid: %v", err)
		}
	})
}

type invalidatingEdge struct{ *fakeEdge }

func (invalidatingEdge) InvalidatesOnPromote() bool { return true }

func invalidatingFake() edge.Edge {
	return invalidatingEdge{&fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustInternal}}}
}

func TestRunCreatesTheInvalidatorForAnEdgeThatInvalidatesOnPromoteAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge edge.Edge
		want bool
	}{
		{"an edge that invalidates on promote", invalidatingFake(), true},
		{"an edge that holds no cache of the origin's", &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustInternal}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			ed := tc.edge

			if err := run(context.Background(), cfn, ssmc, iamc, ed, newFakeObjectStore(), productionSubstrate(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, name := range invalidatorResourceNames {
				if got := strings.Contains(cfn.templates[StackName], name+":"); got != tc.want {
					t.Errorf("template declares %s = %v, want %v", name, got, tc.want)
				}
			}
		})
	}

	t.Run("preview substrate gets one too", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		ed := invalidatingFake()

		if err := run(context.Background(), cfn, ssmc, iamc, ed, newFakeObjectStore(), previewSubstrate(), nil, nil); err != nil {
			t.Fatalf("run: %v", err)
		}
		for _, name := range invalidatorResourceNames {
			if !strings.Contains(cfn.templates[PreviewStackName], name+":") {
				t.Errorf("the preview substrate declares no %s, so a preview's own front never hears a raise", name)
			}
		}
	})

}
