package bootstrap

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cloud/edge"
)

func fixturePublisherPin() artifactPin {
	return artifactPin{version: "4.5.6", sha256: fixtureDigest()}
}

// fixtureArtifacts is a stack with every account-global Lambda already placed,
// which is what the template tests render against.
func fixtureArtifacts() stackArtifacts {
	return stackArtifacts{
		optimizer:   fixtureOptimizerCode(),
		publisher:   fixturePublisherCode(),
		revalidator: fixtureRevalidatorCode(),
	}
}

func fixturePublisherCode() artifactCode {
	return artifactCode{bucket: "ocel-artifacts-test", key: tagPublisherArtifactKey(fixturePublisherPin())}
}

// parsedPublisher is the subset of the rendered template the publisher's own
// resources are asserted against.
type parsedPublisher struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			MessageRetentionPeriod int    `yaml:"MessageRetentionPeriod"`
			SqsManagedSseEnabled   bool   `yaml:"SqsManagedSseEnabled"`
			Runtime                string `yaml:"Runtime"`
			Handler                string `yaml:"Handler"`
			Architectures          []string
			MemorySize             int `yaml:"MemorySize"`
			Timeout                int `yaml:"Timeout"`
			Code                   struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Code"`
			Environment struct {
				Variables map[string]string `yaml:"Variables"`
			} `yaml:"Environment"`
			StartingPosition               string   `yaml:"StartingPosition"`
			BatchSize                      int      `yaml:"BatchSize"`
			MaximumBatchingWindowInSeconds *int     `yaml:"MaximumBatchingWindowInSeconds"`
			FunctionResponseTypes          []string `yaml:"FunctionResponseTypes"`
			MaximumRetryAttempts           *int     `yaml:"MaximumRetryAttempts"`
			DestinationConfig              struct {
				OnFailure struct {
					Destination string `yaml:"Destination"`
				} `yaml:"OnFailure"`
			} `yaml:"DestinationConfig"`
			FilterCriteria struct {
				Filters []struct {
					Pattern string `yaml:"Pattern"`
				} `yaml:"Filters"`
			} `yaml:"FilterCriteria"`
			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect   string `yaml:"Effect"`
						Action   any    `yaml:"Action"`
						Resource any    `yaml:"Resource"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func parsePublisherTemplate(t *testing.T, template string) parsedPublisher {
	t.Helper()
	var tmpl parsedPublisher
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

// TestTagPublisher_ConsumesTheStreamIntoADeadLetterQueue asserts the shape the
// publisher's liveness rests on: a zero batching window so an invalidation is
// not held back, per-record failure reporting and a bounded retry so a poisonous
// record cannot stall its shard or take healthy records down with it, and a
// queue for what those retries could not place.
func TestTagPublisher_ConsumesTheStreamIntoADeadLetterQueue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parsePublisherTemplate(t, tc.template)

			fn, ok := tmpl.Resources["TagPublisher"]
			if !ok {
				t.Fatal("template is missing the TagPublisher function")
			}
			if fn.Type != "AWS::Lambda::Function" {
				t.Errorf("TagPublisher Type = %q, want AWS::Lambda::Function", fn.Type)
			}
			if fn.Properties.Code.S3Bucket != fixturePublisherCode().bucket ||
				fn.Properties.Code.S3Key != fixturePublisherCode().key {
				t.Errorf("TagPublisher Code = %+v, want the placed artifact", fn.Properties.Code)
			}

			esm, ok := tmpl.Resources["TagPublisherStream"]
			if !ok {
				t.Fatal("template is missing the TagPublisherStream event source mapping")
			}
			if esm.Type != "AWS::Lambda::EventSourceMapping" {
				t.Errorf("TagPublisherStream Type = %q, want AWS::Lambda::EventSourceMapping", esm.Type)
			}
			if w := esm.Properties.MaximumBatchingWindowInSeconds; w == nil || *w != 0 {
				t.Errorf("MaximumBatchingWindowInSeconds = %v, want 0 — an invalidation must not wait on a batch filling", w)
			}
			// A batch fans out over the builds it touches, and one of them can be
			// permanently unpublishable — a build whose deploy never initialized
			// the writer 401s forever. Reported per record, only that build's
			// records are retried; without it every healthy build sharing the
			// batch is retried and dead-lettered alongside it.
			if want := []string{"ReportBatchItemFailures"}; !slices.Equal(esm.Properties.FunctionResponseTypes, want) {
				t.Errorf("FunctionResponseTypes = %v, want %v — one poison build must not fail its batch-mates", esm.Properties.FunctionResponseTypes, want)
			}
			if r := esm.Properties.MaximumRetryAttempts; r == nil || *r != tagPublisherRetries {
				t.Errorf("MaximumRetryAttempts = %v, want %d — unbounded retries stall the shard", r, tagPublisherRetries)
			}
			// yaml.v3 hands back a !GetAtt short form as its value alone.
			if got := esm.Properties.DestinationConfig.OnFailure.Destination; got != "TagPublisherDeadLetterQueue.Arn" {
				t.Errorf("OnFailure destination = %q, want the publisher's own dead-letter queue", got)
			}

			dlq, ok := tmpl.Resources["TagPublisherDeadLetterQueue"]
			if !ok {
				t.Fatal("template is missing the TagPublisherDeadLetterQueue")
			}
			if dlq.Type != "AWS::SQS::Queue" {
				t.Errorf("TagPublisherDeadLetterQueue Type = %q, want AWS::SQS::Queue", dlq.Type)
			}
			if !dlq.Properties.SqsManagedSseEnabled {
				t.Error("the dead-letter queue is unencrypted")
			}
		})
	}
}

// TestTagPublisher_FilterConfinesItToTagRecords is the security assertion of
// this whole unit. Upload sessions share the state table and this exact sort
// key, and they carry HMAC secrets — a filter on sk alone would stream them to
// this function. Both key conditions must be present.
func TestTagPublisher_FilterConfinesItToTagRecords(t *testing.T) {
	tmpl := parsePublisherTemplate(t, stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion))
	filters := tmpl.Resources["TagPublisherStream"].Properties.FilterCriteria.Filters
	if len(filters) != 1 {
		t.Fatalf("FilterCriteria.Filters = %+v, want exactly one pattern", filters)
	}

	var pattern struct {
		DynamoDB struct {
			Keys struct {
				PK struct {
					S []map[string]string `json:"S"`
				} `json:"pk"`
				SK struct {
					S []string `json:"S"`
				} `json:"sk"`
			} `json:"Keys"`
		} `json:"dynamodb"`
	}
	if err := json.Unmarshal([]byte(filters[0].Pattern), &pattern); err != nil {
		t.Fatalf("filter pattern is not valid JSON, which Lambda rejects at create time: %v", err)
	}
	if len(pattern.DynamoDB.Keys.PK.S) != 1 || pattern.DynamoDB.Keys.PK.S[0]["prefix"] != "TAG#" {
		t.Errorf("pk rule = %+v, want a TAG# prefix; without it upload sessions reach this function", pattern.DynamoDB.Keys.PK.S)
	}
	if len(pattern.DynamoDB.Keys.SK.S) != 1 || pattern.DynamoDB.Keys.SK.S[0] != "#META" {
		t.Errorf("sk rule = %+v, want #META", pattern.DynamoDB.Keys.SK.S)
	}
}

// TestTagPublisher_RendersNoAlarms. The bootstrap stack is provisioned into
// every account whether or not it has traffic, and it must cost nothing to
// leave idle; a CloudWatch alarm is billed per month per alarm whether or not
// it ever fires. Neither is the mapping opted into the event-source-mapping
// metric group, which is billed the same way and existed only to feed an alarm.
func TestTagPublisher_RendersNoAlarms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, res := range parsePublisherTemplate(t, tc.template).Resources {
				if res.Type == "AWS::CloudWatch::Alarm" {
					t.Errorf("%s is a billed standing alarm in a stack that must be free to leave idle", name)
				}
			}
			if strings.Contains(tc.template, "MetricsConfig") {
				t.Error("the event source mapping is opted into billed mapping metrics that nothing reads")
			}
		})
	}
}

// TestTagPublisher_RoleReachesOnlyWhatItPublishesWith. The function holds the
// authority to derive every build's write secret, so what it can reach beyond
// that has to be exactly its inputs and outputs.
func TestTagPublisher_RoleReachesOnlyWhatItPublishesWith(t *testing.T) {
	for _, tc := range []struct {
		name       string
		template   string
		seedParam  string
		otherParam string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion), ISRWriterSeedParamName, ISRWriterSeedPreviewParamName},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion), ISRWriterSeedPreviewParamName, ISRWriterSeedParamName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parsePublisherTemplate(t, tc.template)
			role, ok := tmpl.Resources["TagPublisherRole"]
			if !ok {
				t.Fatal("template is missing the TagPublisherRole")
			}
			if len(role.Properties.Policies) != 1 {
				t.Fatalf("role policies = %+v, want exactly one inline policy", role.Properties.Policies)
			}
			doc := role.Properties.Policies[0].PolicyDocument
			var actions []string
			for _, st := range doc.Statement {
				switch a := st.Action.(type) {
				case string:
					actions = append(actions, a)
				case []any:
					for _, one := range a {
						actions = append(actions, one.(string))
					}
				}
			}
			for _, forbidden := range []string{"dynamodb:Query", "dynamodb:GetItem", "dynamodb:UpdateItem", "s3:DeleteObject"} {
				for _, got := range actions {
					if got == forbidden {
						t.Errorf("role grants %s; the publisher reads the stream and writes clocks, nothing else", forbidden)
					}
				}
			}

			// The seed is what every build's write secret is derived from, so a
			// preview publisher reaching production's would hold production's
			// entire fleet.
			if !strings.Contains(tc.template, "parameter"+tc.seedParam+"'") {
				t.Errorf("role does not grant %s", tc.seedParam)
			}
			if strings.Contains(tc.template, "parameter"+tc.otherParam+"'") {
				t.Errorf("role reaches the other substrate's seed %s", tc.otherParam)
			}

			env := tmpl.Resources["TagPublisher"].Properties.Environment.Variables
			if env[tagPublisherSeedParamEnvVar] != tc.seedParam {
				t.Errorf("%s = %q, want %q", tagPublisherSeedParamEnvVar, env[tagPublisherSeedParamEnvVar], tc.seedParam)
			}
		})
	}
}

// TestTagPublisher_UnpinnedRendersNothing keeps the same softness the optimizer
// has: a provider build with no cut release renders no publisher at all, rather
// than a function pointing at bytes that are not there. Every other resource
// goes with it — an event source mapping with no function is a stack that
// cannot create.
func TestTagPublisher_UnpinnedRendersNothing(t *testing.T) {
	tmpl := stackTemplate(edge.TrustExternal, stackArtifacts{optimizer: fixtureOptimizerCode()}, RequiredBootstrapVersion)
	for _, name := range []string{
		"TagPublisher", "TagPublisherStream", "TagPublisherRole",
		"TagPublisherDeadLetterQueue",
	} {
		if strings.Contains(tmpl, name+":") {
			t.Errorf("an unpinned build still rendered %s", name)
		}
	}
	if _, err := yaml.Marshal(parsePublisherTemplate(t, tmpl)); err != nil {
		t.Fatalf("template without a publisher is not valid: %v", err)
	}
}
