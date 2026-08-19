package bootstrap

import (
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func fixtureRevalidatorCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(revalidatorKeyPrefix, payloads.Revalidator().SHA256)}
}

type parsedRevalidator struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			QueueName                 string `yaml:"QueueName"`
			FifoQueue                 *bool  `yaml:"FifoQueue"`
			ContentBasedDeduplication *bool  `yaml:"ContentBasedDeduplication"`
			VisibilityTimeout         int    `yaml:"VisibilityTimeout"`
			MessageRetentionPeriod    int    `yaml:"MessageRetentionPeriod"`
			KmsMasterKeyId            string `yaml:"KmsMasterKeyId"`
			RedrivePolicy             struct {
				DeadLetterTargetArn string `yaml:"deadLetterTargetArn"`
				MaxReceiveCount     int    `yaml:"maxReceiveCount"`
			} `yaml:"RedrivePolicy"`

			Runtime       string   `yaml:"Runtime"`
			Handler       string   `yaml:"Handler"`
			Architectures []string `yaml:"Architectures"`
			MemorySize    int      `yaml:"MemorySize"`
			Timeout       int      `yaml:"Timeout"`
			Code          struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Code"`
			Environment struct {
				Variables map[string]string `yaml:"Variables"`
			} `yaml:"Environment"`

			EventSourceArn        string   `yaml:"EventSourceArn"`
			FunctionName          string   `yaml:"FunctionName"`
			BatchSize             int      `yaml:"BatchSize"`
			FunctionResponseTypes []string `yaml:"FunctionResponseTypes"`
			MaximumConcurrency    *int     `yaml:"MaximumConcurrency"`
			ScalingConfig         struct {
				MaximumConcurrency *int `yaml:"MaximumConcurrency"`
			} `yaml:"ScalingConfig"`

			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []policyStatement `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

type policyStatement struct {
	Effect    string         `yaml:"Effect"`
	Action    any            `yaml:"Action"`
	Resource  any            `yaml:"Resource"`
	Condition map[string]any `yaml:"Condition"`
}

func (s policyStatement) actions() []string   { return yamlStrings(s.Action) }
func (s policyStatement) resources() []string { return yamlStrings(s.Resource) }

func yamlStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, one := range t {
			if s, ok := one.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func parseRevalidatorTemplate(t *testing.T, template string) parsedRevalidator {
	t.Helper()
	var tmpl parsedRevalidator
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func revalidatorTemplates() []struct {
	name         string
	class        string
	template     string
	edgeTemplate string
} {
	return []struct {
		name         string
		class        string
		template     string
		edgeTemplate string
	}{
		{"production", ClassProduction, featureTemplate(FeatureISR, ClassProduction), featureTemplate(FeatureCloudflareEdge, ClassProduction)},
		{"preview", ClassPreview, featureTemplate(FeatureISR, ClassPreview), featureTemplate(FeatureCloudflareEdge, ClassPreview)},
	}
}

func TestRevalidateQueue(t *testing.T) {
	t.Run("dedupes renders and retires poison", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parseRevalidatorTemplate(t, tc.template)
				wantQueue, wantDLQ := revalidateQueueNames(tc.class)

				q, ok := tmpl.Resources["RevalidateQueue"]
				if !ok {
					t.Fatal("template is missing the RevalidateQueue")
				}
				if q.Type != "AWS::SQS::Queue" {
					t.Errorf("RevalidateQueue Type = %q, want AWS::SQS::Queue", q.Type)
				}
				if q.Properties.QueueName != wantQueue {
					t.Errorf("QueueName = %q, want %q", q.Properties.QueueName, wantQueue)
				}
				if q.Properties.FifoQueue == nil || !*q.Properties.FifoQueue {
					t.Error("RevalidateQueue is not FIFO; the deduplication that collapses the herd is a FIFO feature")
				}
				if q.Properties.ContentBasedDeduplication == nil || *q.Properties.ContentBasedDeduplication {
					t.Error("ContentBasedDeduplication must be explicitly false: the sender supplies an id derived from the render it wants, not a hash of the body")
				}
				if got := q.Properties.VisibilityTimeout; got != revalidateVisibilityTimeoutSeconds {
					t.Errorf("VisibilityTimeout = %d, want %d", got, revalidateVisibilityTimeoutSeconds)
				}
				if got := q.Properties.MessageRetentionPeriod; got != revalidateRetentionSeconds {
					t.Errorf("MessageRetentionPeriod = %d, want %d", got, revalidateRetentionSeconds)
				}
				if got := q.Properties.RedrivePolicy.DeadLetterTargetArn; got != "RevalidateDeadLetterQueue.Arn" {
					t.Errorf("redrive target = %q, want the revalidation dead-letter queue", got)
				}
				if got := q.Properties.RedrivePolicy.MaxReceiveCount; got != revalidateMaxReceiveCount {
					t.Errorf("maxReceiveCount = %d, want %d", got, revalidateMaxReceiveCount)
				}

				dlq, ok := tmpl.Resources["RevalidateDeadLetterQueue"]
				if !ok {
					t.Fatal("template is missing the RevalidateDeadLetterQueue")
				}
				if dlq.Properties.QueueName != wantDLQ {
					t.Errorf("dead-letter QueueName = %q, want %q", dlq.Properties.QueueName, wantDLQ)
				}
				if dlq.Properties.FifoQueue == nil || !*dlq.Properties.FifoQueue {
					t.Error("the dead-letter queue is not FIFO, which a FIFO source cannot redrive to")
				}
				if got := dlq.Properties.MessageRetentionPeriod; got != revalidateDLQRetentionSeconds {
					t.Errorf("dead-letter MessageRetentionPeriod = %d, want %d", got, revalidateDLQRetentionSeconds)
				}
			})
		}
	})

	t.Run("is encrypted under a managed key", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parseRevalidatorTemplate(t, tc.template)
				for _, name := range []string{"RevalidateQueue", "RevalidateDeadLetterQueue"} {
					if got := tmpl.Resources[name].Properties.KmsMasterKeyId; got != "alias/aws/sqs" {
						t.Errorf("%s KmsMasterKeyId = %q, want the SSE-KMS managed key; the messages carry bypass tokens", name, got)
					}
				}
			})
		}
	})

	t.Run("both ends can use the envelope", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				user, ok := parseRevalidatorTemplate(t, tc.edgeTemplate).Resources["EdgeUser"]
				if !ok {
					t.Fatal("template is missing the EdgeUser")
				}
				for _, end := range []struct {
					who        string
					statements []policyStatement
					want       []string
				}{
					{"the edge producer", user.Properties.Policies[0].PolicyDocument.Statement, []string{"kms:GenerateDataKey", "kms:Decrypt"}},
					{"the consumer", revalidatorPolicy(t, tc.template), []string{"kms:Decrypt"}},
				} {
					var kms []policyStatement
					for _, st := range end.statements {
						if slices.ContainsFunc(st.actions(), func(a string) bool { return strings.HasPrefix(a, "kms:") }) {
							kms = append(kms, st)
						}
					}
					if len(kms) != 1 {
						t.Fatalf("%s holds %d KMS statements, want exactly one; without it every message against the SSE-KMS queue fails KMS.AccessDeniedException and the queue silently stays empty", end.who, len(kms))
					}
					st := kms[0]
					if !slices.Equal(st.actions(), end.want) {
						t.Errorf("%s KMS actions = %v, want exactly %v", end.who, st.actions(), end.want)
					}
					if got := st.resources(); !slices.Equal(got, []string{"*"}) {
						t.Errorf("%s KMS resource = %v, want '*' — the managed alias/aws/sqs key's ARN is not knowable in the template, so the condition is what bounds this", end.who, got)
					}
					equals, ok := st.Condition["StringEquals"].(map[string]any)
					if !ok {
						t.Fatalf("%s KMS condition = %+v, want a StringEquals on kms:ViaService; unconditioned, this is an account-wide KMS grant", end.who, st.Condition)
					}
					if want := "sqs.${AWS::Region}.amazonaws.com"; equals["kms:ViaService"] != want {
						t.Errorf("%s kms:ViaService = %v, want %q — anything wider lets this key decrypt outside SQS", end.who, equals["kms:ViaService"], want)
					}
					if len(equals) != 1 || len(st.Condition) != 1 {
						t.Errorf("%s KMS condition = %+v, want kms:ViaService alone", end.who, st.Condition)
					}
				}
			})
		}
	})
}

func TestRevalidator(t *testing.T) {
	t.Run("drains the queue at a bounded concurrency", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parseRevalidatorTemplate(t, tc.template)
				esm, ok := tmpl.Resources["RevalidatorQueueConsumer"]
				if !ok {
					t.Fatal("template is missing the RevalidatorQueueConsumer event source mapping")
				}
				if esm.Type != "AWS::Lambda::EventSourceMapping" {
					t.Errorf("RevalidatorQueueConsumer Type = %q, want AWS::Lambda::EventSourceMapping", esm.Type)
				}
				if got := esm.Properties.EventSourceArn; got != "RevalidateQueue.Arn" {
					t.Errorf("EventSourceArn = %q, want the revalidation queue", got)
				}
				if got := esm.Properties.BatchSize; got != revalidatorBatchSize {
					t.Errorf("BatchSize = %d, want %d", got, revalidatorBatchSize)
				}
				if want := []string{"ReportBatchItemFailures"}; !slices.Equal(esm.Properties.FunctionResponseTypes, want) {
					t.Errorf("FunctionResponseTypes = %v, want %v", esm.Properties.FunctionResponseTypes, want)
				}
				if c := esm.Properties.ScalingConfig.MaximumConcurrency; c == nil || *c != revalidatorMaxConcurrency {
					t.Errorf("ScalingConfig.MaximumConcurrency = %v, want %d — the render-drain bound", c, revalidatorMaxConcurrency)
				}
				if esm.Properties.MaximumConcurrency != nil {
					t.Error("MaximumConcurrency is rendered at the top level, where CloudFormation does not take it; nest it under ScalingConfig")
				}
			})
		}
	})

	t.Run("timeout fits inside the visibility timeout", func(t *testing.T) {
		if revalidatorTimeoutSeconds >= revalidateVisibilityTimeoutSeconds {
			t.Fatalf("function timeout %ds does not fit inside the %ds visibility timeout", revalidatorTimeoutSeconds, revalidateVisibilityTimeoutSeconds)
		}
		const worstCaseBatchSeconds = 120
		if revalidatorTimeoutSeconds < worstCaseBatchSeconds {
			t.Errorf("function timeout %ds clips the %ds worst-case batch the package is sized for", revalidatorTimeoutSeconds, worstCaseBatchSeconds)
		}
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				fn := parseRevalidatorTemplate(t, tc.template).Resources["Revalidator"]
				if fn.Properties.Timeout != revalidatorTimeoutSeconds {
					t.Errorf("Timeout = %d, want %d", fn.Properties.Timeout, revalidatorTimeoutSeconds)
				}
			})
		}
	})

	t.Run("reads only origin records", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				for _, st := range revalidatorPolicy(t, tc.template) {
					if !slices.Contains(st.actions(), "s3:GetObject") {
						continue
					}
					if want := []string{"${AssetBucketArn}/*/origin.json"}; !slices.Equal(st.resources(), want) {
						t.Fatalf("s3:GetObject is granted on %v, want %v", st.resources(), want)
					}
					return
				}
				t.Fatal("the revalidator role cannot read the origin record it resolves every trigger from")
			})
		}
	})

	t.Run("reaches only what it triggers with", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				var actions []string
				for _, st := range revalidatorPolicy(t, tc.template) {
					actions = append(actions, st.actions()...)
				}
				want := []string{
					"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes",
					"kms:Decrypt", "s3:GetObject",
					"lambda:InvokeFunctionUrl", "lambda:InvokeFunction",
				}
				if !slices.Equal(actions, want) {
					t.Errorf("revalidator role grants %v, want exactly %v", actions, want)
				}

				env := parseRevalidatorTemplate(t, tc.template).Resources["Revalidator"].Properties.Environment.Variables
				if got := env[revalidatorAssetBucketEnvVar]; got != paramAssetBucketName {
					t.Errorf("%s = %q, want a !Ref of this substrate's own asset bucket; unset, the consumer resolves nothing and triggers nothing", revalidatorAssetBucketEnvVar, got)
				}
			})
		}
	})

	t.Run("invoke grant is account wide over app functions alone", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				for _, st := range revalidatorPolicy(t, tc.template) {
					if !slices.Contains(st.actions(), "lambda:InvokeFunctionUrl") {
						continue
					}
					if !slices.Contains(st.actions(), "lambda:InvokeFunction") {
						t.Errorf("invoke grant = %v, want both lambda:InvokeFunctionUrl and lambda:InvokeFunction", st.actions())
					}
					want := []string{"arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*"}
					if !slices.Equal(st.resources(), want) {
						t.Errorf("invoke grant resource = %v, want %v — the edge user's own '*' region is wider than this consumer needs", st.resources(), want)
					}
					equals, ok := st.Condition["StringEquals"].(map[string]any)
					if !ok {
						t.Fatalf("invoke grant condition = %+v, want a StringEquals on the ocel:component tag", st.Condition)
					}
					if got := equals["aws:ResourceTag/ocel:component"]; got != "function" {
						t.Errorf("invoke condition = %v, want 'aws:ResourceTag/ocel:component': 'function' — anything looser reaches the bucket listeners too", got)
					}
					return
				}
				t.Fatal("the revalidator role cannot invoke the Function URLs it triggers")
			})
		}
	})

	t.Run("renders no alarms", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				for name, res := range parseRevalidatorTemplate(t, tc.template).Resources {
					if res.Type == "AWS::CloudWatch::Alarm" {
						t.Errorf("%s is a billed standing alarm in a stack that must be free to leave idle", name)
					}
				}
			})
		}
	})

	t.Run("a placed revalidator publishes the queue URL", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				out, ok := parseRevalidatorTemplate(t, tc.template).Outputs[outputRevalidateQueueURL]
				if !ok {
					t.Fatalf("template publishes no %s", outputRevalidateQueueURL)
				}
				if out.Value != "RevalidateQueue" {
					t.Errorf("%s = %q, want a !Ref of the queue, which is its URL", outputRevalidateQueueURL, out.Value)
				}
			})
		}
	})
}

func iamResourceMatches(pattern, arn string) bool {
	parts := strings.Split(pattern, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(arn)
}

func TestAssetBucketRevalidator(t *testing.T) {
	t.Run("no key satisfies both the edge write and the origin read", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				write := edgeGrant(t, tc.edgeTemplate, "s3:PutObject")
				read := revalidatorGrant(t, tc.template, "s3:GetObject")

				for _, key := range []string{
					"${AssetBucketArn}/prod/proj/web/B1/fetch-cache/origin.json",
					"${AssetBucketArn}/prod/proj/web/B1/origin.json",
					"${AssetBucketArn}/prod/proj/web/B1/fetch-cache/a/origin.json",
				} {
					if iamResourceMatches(write, key) && iamResourceMatches(read, key) {
						t.Errorf("%s is both edge-writable under %q and consumer-readable under %q: a stolen edge key plants an origin record and the consumer delivers the app's bypass token to it", key, write, read)
					}
				}

				writeTail := write[strings.LastIndex(write, "*")+1:]
				readTail := read[strings.LastIndex(read, "*")+1:]
				if writeTail == "" || readTail == "" {
					t.Fatalf("one grant ends in an unanchored wildcard (write %q, read %q); nothing bounds what a key can end in, so the two grants cannot be disjoint", write, read)
				}
				if strings.HasSuffix(writeTail, readTail) || strings.HasSuffix(readTail, writeTail) {
					t.Errorf("write grant ends %q and read grant ends %q; one is a suffix of the other, so a single key satisfies both", writeTail, readTail)
				}
			})
		}
	})
}

func edgeGrant(t *testing.T, template, action string) string {
	t.Helper()
	user, ok := parseRevalidatorTemplate(t, template).Resources["EdgeUser"]
	if !ok {
		t.Fatal("template is missing the EdgeUser")
	}
	return soleResource(t, user.Properties.Policies[0].PolicyDocument.Statement, action)
}

func revalidatorGrant(t *testing.T, template, action string) string {
	t.Helper()
	return soleResource(t, revalidatorPolicy(t, template), action)
}

func soleResource(t *testing.T, statements []policyStatement, action string) string {
	t.Helper()
	var found []string
	for _, st := range statements {
		if slices.Contains(st.actions(), action) {
			found = append(found, st.resources()...)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s is granted on %v, want exactly one resource", action, found)
	}
	return found[0]
}

func TestRunRevalidator(t *testing.T) {
	t.Run("this build bootstraps a consumer", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			run       func(context.Context, APIs, Request, func(string), func(string)) error
			stackName string
		}{
			{"production", Run, isrStack(ClassProduction)},
			{"preview", RunPreview, isrStack(ClassPreview)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				standInCloudflare(t, &fakeEdge{kind: "cloudflare"})

				if err := tc.run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), everything(), nil, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				template := cfn.template(tc.stackName)
				for _, name := range []string{
					"Revalidator", "RevalidatorRole", "RevalidatorQueueConsumer",
				} {
					if !strings.Contains(template, "  "+name+":") {
						t.Errorf("this build's bootstrap rendered no %s", name)
					}
				}
				if want := payloads.Key(revalidatorKeyPrefix, payloads.Revalidator().SHA256); !strings.Contains(template, want) {
					t.Errorf("the consumer does not read its code from %s", want)
				}
				if _, ok := parseRevalidatorTemplate(t, template).Outputs[outputRevalidateQueueURL]; !ok {
					t.Errorf("this build's bootstrap published no %s, so the edge keeps rendering through the origin", outputRevalidateQueueURL)
				}
			})
		}
	})
}

func revalidatorPolicy(t *testing.T, template string) []policyStatement {
	t.Helper()
	role, ok := parseRevalidatorTemplate(t, template).Resources["RevalidatorRole"]
	if !ok {
		t.Fatal("template is missing the RevalidatorRole")
	}
	if len(role.Properties.Policies) != 1 {
		t.Fatalf("role policies = %+v, want exactly one inline policy", role.Properties.Policies)
	}
	return role.Properties.Policies[0].PolicyDocument.Statement
}

func TestEdgeUserRevalidator(t *testing.T) {
	t.Run("sends to the queue and nothing else", func(t *testing.T) {
		for _, tc := range revalidatorTemplates() {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parseRevalidatorTemplate(t, tc.edgeTemplate)
				user, ok := tmpl.Resources["EdgeUser"]
				if !ok {
					t.Fatal("template is missing the EdgeUser")
				}
				if len(user.Properties.Policies) != 1 {
					t.Fatalf("edge user policies = %+v, want exactly one inline policy", user.Properties.Policies)
				}

				var sqsActions []string
				var sendResources []string
				for _, st := range user.Properties.Policies[0].PolicyDocument.Statement {
					for _, a := range st.actions() {
						if strings.HasPrefix(a, "sqs:") {
							sqsActions = append(sqsActions, a)
							sendResources = append(sendResources, st.resources()...)
						}
					}
				}
				if want := []string{"sqs:SendMessage"}; !slices.Equal(sqsActions, want) {
					t.Errorf("edge user SQS actions = %v, want exactly %v", sqsActions, want)
				}
				if want := []string{paramRevalidateQueueARN}; !slices.Equal(sendResources, want) {
					t.Errorf("edge user sends to %v, want only the revalidation queue's own ARN", sendResources)
				}
			})
		}
	})
}

func TestEnsureRevalidatorPayload(t *testing.T) {
	t.Run("uploads the embedded revalidator", func(t *testing.T) {
		store := newFakeObjectStore()

		code, err := ensureRevalidatorPayload(context.Background(), store, fixtureBucket)
		if err != nil {
			t.Fatalf("ensureRevalidatorPayload: %v", err)
		}
		if want := payloads.Key(revalidatorKeyPrefix, payloads.Revalidator().SHA256); code.Key != want {
			t.Errorf("key = %q, want %q", code.Key, want)
		}
		if !bytes.Equal(store.objects[code.Key], payloads.Revalidator().Bytes) {
			t.Error("the account holds bytes other than the embedded revalidator")
		}
		if want := payloads.Revalidator().ChecksumSHA256; len(store.putChecksums) != 1 || store.putChecksums[0] != want {
			t.Errorf("uploaded with checksums %v, want [%s]", store.putChecksums, want)
		}
	})
}
