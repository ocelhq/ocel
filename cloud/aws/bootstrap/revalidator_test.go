package bootstrap

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cloud/edge"
)

func fixtureRevalidatorPin() artifactPin {
	return artifactPin{version: "7.8.9", sha256: fixtureDigest()}
}

func fixtureRevalidatorCode() artifactCode {
	return artifactCode{bucket: "ocel-artifacts-test", key: revalidatorArtifactKey(fixtureRevalidatorPin())}
}

// parsedRevalidator is the subset of the rendered template the revalidation
// queue and its consumer are asserted against.
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

			Namespace          string `yaml:"Namespace"`
			MetricName         string `yaml:"MetricName"`
			Statistic          string `yaml:"Statistic"`
			Threshold          int    `yaml:"Threshold"`
			Period             int    `yaml:"Period"`
			EvaluationPeriods  int    `yaml:"EvaluationPeriods"`
			ComparisonOperator string `yaml:"ComparisonOperator"`
			TreatMissingData   string `yaml:"TreatMissingData"`
			Dimensions         []struct {
				Name  string `yaml:"Name"`
				Value string `yaml:"Value"`
			} `yaml:"Dimensions"`

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

// policyStatement is one Allow in an inline policy, in the two shapes the
// templates write actions and resources in (a scalar or a list).
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
	name     string
	class    string
	template string
} {
	return []struct {
		name     string
		class    string
		template string
	}{
		{"production", ClassProduction, stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", ClassPreview, previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
	}
}

// TestRevalidateQueue_DedupesRendersAndRetiresPoison asserts the two amended
// constants by value — they are the ones a plausible-looking edit moves — plus
// the FIFO shape the deduplication rests on and the redrive that keeps a record
// nothing can render from cycling forever.
func TestRevalidateQueue_DedupesRendersAndRetiresPoison(t *testing.T) {
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
			// Human decision G. A batch of ten at the handler's per-record budget
			// is up to 120s of work; at 60 the consumer redelivers and eventually
			// dead-letters records it already processed successfully.
			if got := q.Properties.VisibilityTimeout; got != revalidateVisibilityTimeoutSeconds {
				t.Errorf("VisibilityTimeout = %d, want %d", got, revalidateVisibilityTimeoutSeconds)
			}
			// Human decision C. At an hour, a wedged consumer accumulates a dozen
			// distinctly-deduped stale echoes per route and renders every one of
			// them on recovery.
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
			// A FIFO source may only redrive to a FIFO queue.
			if dlq.Properties.FifoQueue == nil || !*dlq.Properties.FifoQueue {
				t.Error("the dead-letter queue is not FIFO, which a FIFO source cannot redrive to")
			}
			if got := dlq.Properties.MessageRetentionPeriod; got != revalidateDLQRetentionSeconds {
				t.Errorf("dead-letter MessageRetentionPeriod = %d, want %d", got, revalidateDLQRetentionSeconds)
			}
		})
	}
}

// TestRevalidateQueue_IsEncryptedUnderAManagedKey. Every message carries the
// app's bypass token in x-prerender-revalidate, so the queue and its
// dead-letter queue are both secret-bearing stores; SQS-managed encryption
// would leave their use unauditable.
func TestRevalidateQueue_IsEncryptedUnderAManagedKey(t *testing.T) {
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
}

// TestRevalidateQueue_RendersWithoutAConsumer. The queue is the edge's send
// target and the thing the edge user is granted against, so it exists in every
// substrate — a build that pins no consumer still renders it.
func TestRevalidateQueue_RendersWithoutAConsumer(t *testing.T) {
	unpinned := stackArtifacts{optimizer: fixtureOptimizerCode()}
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, unpinned, RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, unpinned, RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"RevalidateQueue", "RevalidateDeadLetterQueue"} {
				if !strings.Contains(tc.template, "  "+name+":") {
					t.Errorf("an unpinned build rendered no %s", name)
				}
			}
		})
	}
}

// TestRevalidator_DrainsTheQueueAtABoundedConcurrency. MaximumConcurrency is
// asserted through ScalingConfig on purpose: CloudFormation takes it as a
// sub-property there and nowhere else, and a top-level one is accepted by the
// YAML and dropped, which silently removes the only cap this system has on how
// many renders a mass invalidation can put on the origin at once.
func TestRevalidator_DrainsTheQueueAtABoundedConcurrency(t *testing.T) {
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
			// The handler reports per-record outcomes; without this the whole
			// batch is redelivered whenever any one route cannot be rendered.
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
}

// TestRevalidator_TimeoutFitsInsideTheVisibilityTimeout. The two numbers are one
// decision: a function that can outlast the visibility timeout has SQS
// redelivering records it is still processing, and dead-lettering renders that
// succeeded. packages/revalidator/src/limits.mts sizes the batch's worst case at
// 120s and its test/limits.test.mts asserts that; this is the template's half.
func TestRevalidator_TimeoutFitsInsideTheVisibilityTimeout(t *testing.T) {
	if revalidatorTimeoutSeconds >= revalidateVisibilityTimeoutSeconds {
		t.Fatalf("function timeout %ds does not fit inside the %ds visibility timeout", revalidatorTimeoutSeconds, revalidateVisibilityTimeoutSeconds)
	}
	const worstCaseBatchSeconds = 120 // limits.mts: 10 records x (10s trigger + 2s record read)
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
}

// TestRevalidator_ReadsOnlyOriginRecords is the IAM half of the exfiltration
// defence. The message names the key this role reads, and the edge can write
// fully-controlled JSON under the asset bucket's fetch-cache segment. IAM's '*'
// spans '/', so anchoring this grant on the '/origin.json' suffix is half of
// what keeps it disjoint from that write grant — the other half is asserted by
// TestAssetBucket_NoKeySatisfiesBothTheEdgeWriteAndTheOriginRead, which is the
// test that actually establishes the disjointness. Relaxing this Resource to
// '/*' re-opens a demonstrated token exfiltration.
func TestRevalidator_ReadsOnlyOriginRecords(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			for _, st := range revalidatorPolicy(t, tc.template) {
				if !slices.Contains(st.actions(), "s3:GetObject") {
					continue
				}
				if want := []string{"${AssetBucket.Arn}/*/origin.json"}; !slices.Equal(st.resources(), want) {
					t.Fatalf("s3:GetObject is granted on %v, want %v", st.resources(), want)
				}
				return
			}
			t.Fatal("the revalidator role cannot read the origin record it resolves every trigger from")
		})
	}
}

// iamResourceMatches answers whether an IAM resource pattern admits a key,
// under IAM's own wildcard semantics: '*' matches any run of characters,
// including '/'. That last part is the whole reason this test exists — the
// intuition that a '*' stops at a path separator is what makes two grants look
// disjoint when they are not.
func iamResourceMatches(pattern, arn string) bool {
	parts := strings.Split(pattern, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(arn)
}

// TestAssetBucket_NoKeySatisfiesBothTheEdgeWriteAndTheOriginRead is the
// disjointness the exfiltration defence is built on, asserted as a property of
// the two rendered IAM patterns rather than as a claim about the worker's code.
//
// The threat is a STOLEN EDGE CREDENTIAL, which is not bound by what
// workers/nextjs writes. So "every key the edge worker writes ends .cache.json"
// proves nothing here; what has to hold is that no key at all satisfies both
// grants. Both patterns are anchored on a trailing literal, and neither literal
// is a suffix of the other, so no key can end in both — that is the mechanism,
// and it survives any regression in the worker or in the consumer's parser.
//
// The named key below is the exact one that defeated the earlier, unanchored
// write grant '${AssetBucket.Arn}/*/fetch-cache/*': it is writable by the edge
// (first '*' = the build prefix, second '*' = 'origin.json') and readable by the
// consumer (its '*' = the prefix plus the fetch-cache segment). Planting it
// names an attacker-controlled origin for a victim app, and the consumer signs
// and delivers that app's bypass token to it.
func TestAssetBucket_NoKeySatisfiesBothTheEdgeWriteAndTheOriginRead(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			write := edgeGrant(t, tc.template, "s3:PutObject")
			read := revalidatorGrant(t, tc.template, "s3:GetObject")

			for _, key := range []string{
				"${AssetBucket.Arn}/prod/proj/web/B1/fetch-cache/origin.json",
				"${AssetBucket.Arn}/prod/proj/web/B1/origin.json",
				"${AssetBucket.Arn}/prod/proj/web/B1/fetch-cache/a/origin.json",
			} {
				if iamResourceMatches(write, key) && iamResourceMatches(read, key) {
					t.Errorf("%s is both edge-writable under %q and consumer-readable under %q: a stolen edge key plants an origin record and the consumer delivers the app's bypass token to it", key, write, read)
				}
			}

			// The general statement, not just the three keys above: a key
			// admitted by both would have to end in both patterns' trailing
			// literals at once.
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
}

// edgeGrant returns the single Resource the edge user's policy grants action on.
func edgeGrant(t *testing.T, template, action string) string {
	t.Helper()
	user, ok := parseRevalidatorTemplate(t, template).Resources["EdgeUser"]
	if !ok {
		t.Fatal("template is missing the EdgeUser")
	}
	return soleResource(t, user.Properties.Policies[0].PolicyDocument.Statement, action)
}

// revalidatorGrant returns the single Resource the consumer role grants action on.
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

// TestRevalidateQueue_BothEndsCanUseTheEnvelope covers the grants that the
// queue's SSE-KMS envelope makes mandatory rather than optional. AWS requires a
// producer against an SSE-KMS queue to hold kms:GenerateDataKey* AND kms:Decrypt
// in its own policy, and a consumer to hold kms:Decrypt; without them every
// SendMessage fails with KMS.AccessDeniedException.
//
// That failure is silent in exactly the epic's signature shape: the edge fails
// open to originBlocking, the queue never receives anything, and an empty queue
// is the documented healthy state, so no alarm fires. Nothing else in the suite
// notices — TestEdgeUser_SendsToTheQueueAndNothingElse filters to sqs: actions,
// so the whole statement could be deleted and pass.
//
// Resource is '*' on both ends and that is the only workable form: the AWS-
// managed alias/aws/sqs key's ARN is not knowable in the template. The
// kms:ViaService condition is what bounds it instead — it confines the grant to
// calls SQS itself makes on the principal's behalf — so the condition is
// asserted as tightly as the actions are. Widening it is what widens the grant.
func TestRevalidateQueue_BothEndsCanUseTheEnvelope(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			user, ok := parseRevalidatorTemplate(t, tc.template).Resources["EdgeUser"]
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
}

// TestRevalidator_ReachesOnlyWhatItTriggersWith. The function holds every app's
// bypass token in transit, so what it can reach has to be its queue, its record
// and the functions it triggers — and nothing that writes.
func TestRevalidator_ReachesOnlyWhatItTriggersWith(t *testing.T) {
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
			if got := env[revalidatorAssetBucketEnvVar]; got != "AssetBucket" {
				t.Errorf("%s = %q, want a !Ref of this substrate's own asset bucket; unset, the consumer resolves nothing and triggers nothing", revalidatorAssetBucketEnvVar, got)
			}
		})
	}
}

// TestRevalidator_InvokeGrantIsAccountWideOverOcelTaggedFunctions records a
// resolution rather than merely asserting a string. The design doc describes
// this condition as "scoped by the ocel:app resource tag", which reads narrower
// than it is: Null-on-the-tag means "carries one at all", over every function in
// the account. That looseness is ACCEPTED here, not overlooked — there is one
// consumer per substrate and it triggers renders for every app in it, so there
// is no app to scope to, and the functions are Pulumi-autonamed, so there is no
// name to scope to either. It is narrowed on the one axis available: this
// account and this region, where the deploy path creates the Function URLs it
// triggers.
func TestRevalidator_InvokeGrantIsAccountWideOverOcelTaggedFunctions(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			for _, st := range revalidatorPolicy(t, tc.template) {
				if !slices.Contains(st.actions(), "lambda:InvokeFunctionUrl") {
					continue
				}
				// Both actions are required since AWS's October 2025 change.
				if !slices.Contains(st.actions(), "lambda:InvokeFunction") {
					t.Errorf("invoke grant = %v, want both lambda:InvokeFunctionUrl and lambda:InvokeFunction", st.actions())
				}
				want := []string{"arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*"}
				if !slices.Equal(st.resources(), want) {
					t.Errorf("invoke grant resource = %v, want %v — the edge user's own '*' region is wider than this consumer needs", st.resources(), want)
				}
				null, ok := st.Condition["Null"].(map[string]any)
				if !ok {
					t.Fatalf("invoke grant condition = %+v, want a Null condition on the ocel:app tag", st.Condition)
				}
				if got := null["aws:ResourceTag/ocel:app"]; got != "false" {
					t.Errorf("invoke condition = %v, want 'aws:ResourceTag/ocel:app': 'false'", got)
				}
				return
			}
			t.Fatal("the revalidator role cannot invoke the Function URLs it triggers")
		})
	}
}

// TestRevalidator_Alarms covers the three ways revalidation stops. There is
// deliberately no absence alarm on the poller: unlike the publisher's stream, an
// empty revalidation queue is the healthy steady state, so silence here is not a
// signal and an idle substrate must not alarm.
func TestRevalidator_Alarms(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseRevalidatorTemplate(t, tc.template)
			for _, want := range []struct {
				resource   string
				namespace  string
				metric     string
				dimension  string
				threshold  int
				comparison string
			}{
				{"RevalidatorDeadLetterAlarm", "AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", 0, "GreaterThanThreshold"},
				{"RevalidatorBacklogAlarm", "AWS/SQS", "ApproximateAgeOfOldestMessage", "QueueName", revalidatorOldestMessageThresholdSeconds, "GreaterThanThreshold"},
				{"RevalidatorErrorAlarm", "AWS/Lambda", "Errors", "FunctionName", 0, "GreaterThanThreshold"},
			} {
				alarm, ok := tmpl.Resources[want.resource]
				if !ok {
					t.Errorf("template is missing the %s", want.resource)
					continue
				}
				p := alarm.Properties
				if alarm.Type != "AWS::CloudWatch::Alarm" {
					t.Errorf("%s Type = %q, want AWS::CloudWatch::Alarm", want.resource, alarm.Type)
				}
				if p.Namespace != want.namespace || p.MetricName != want.metric {
					t.Errorf("%s metric = %s/%s, want %s/%s", want.resource, p.Namespace, p.MetricName, want.namespace, want.metric)
				}
				if p.Threshold != want.threshold || p.ComparisonOperator != want.comparison {
					t.Errorf("%s fires at %s %d, want %s %d", want.resource, p.ComparisonOperator, p.Threshold, want.comparison, want.threshold)
				}
				if p.Period != revalidatorAlarmPeriodSeconds || p.EvaluationPeriods != revalidatorAlarmPeriods {
					t.Errorf("%s evaluates %d x %ds, want %d x %ds", want.resource, p.EvaluationPeriods, p.Period, revalidatorAlarmPeriods, revalidatorAlarmPeriodSeconds)
				}
				if p.TreatMissingData != "notBreaching" {
					t.Errorf("%s TreatMissingData = %q, want notBreaching — an idle substrate publishes no datapoints and is healthy", want.resource, p.TreatMissingData)
				}
				if len(p.Dimensions) != 1 || p.Dimensions[0].Name != want.dimension {
					t.Errorf("%s Dimensions = %+v, want %s", want.resource, p.Dimensions, want.dimension)
				}
			}
			// Against this block alone: the tag publisher renders both metrics
			// in the same template, and its stream is the case where silence
			// really is the signal.
			for _, absent := range []string{"PolledEventCount", "FailedInvokeEventCount"} {
				if strings.Contains(revalidatorResources(fixtureRevalidatorCode()), absent) {
					t.Errorf("the revalidator alarms on %s; an empty revalidation queue is the healthy steady state", absent)
				}
			}
		})
	}
}

// TestRevalidator_UnpinnedRendersNoConsumerAndNoQueueURL is human decision F and
// the unpinned-skip path in one. A build with no cut release renders no consumer
// — an event source mapping with no function is a stack that cannot create —
// and, critically, publishes no queue URL: the queue is still there, and an edge
// told about a queue nothing drains enqueues successfully, reports the refresh
// landed, re-arms its colo sentinel, and stops revalidating the route until it
// hard-expires, with nothing anywhere reporting a failure.
func TestRevalidator_UnpinnedRendersNoConsumerAndNoQueueURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, stackArtifacts{optimizer: fixtureOptimizerCode(), publisher: fixturePublisherCode()}, RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, stackArtifacts{optimizer: fixtureOptimizerCode(), publisher: fixturePublisherCode()}, RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{
				"Revalidator", "RevalidatorRole", "RevalidatorQueueConsumer",
				"RevalidatorDeadLetterAlarm", "RevalidatorBacklogAlarm", "RevalidatorErrorAlarm",
			} {
				if strings.Contains(tc.template, "  "+name+":") {
					t.Errorf("an unpinned build still rendered %s", name)
				}
			}
			if _, ok := parseRevalidatorTemplate(t, tc.template).Outputs[outputRevalidateQueueURL]; ok {
				t.Errorf("an unpinned build published %s; the edge would enqueue into a queue nothing drains and report the refresh landed", outputRevalidateQueueURL)
			}
		})
	}
}

// TestRevalidator_PinnedPublishesTheQueueURL is decision F's other half: with a
// consumer rendered, the edge is told where to send.
func TestRevalidator_PinnedPublishesTheQueueURL(t *testing.T) {
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
}

// revalidatorPolicy is the consumer role's single inline policy document.
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

// TestEdgeUser_SendsToTheQueueAndNothingElse. The edge enqueues and never
// drains: a receive or delete grant on this key would let a compromised edge
// swallow the fleet's revalidations, which fails in the epic's signature shape
// — silently, until every route hard-expires.
func TestEdgeUser_SendsToTheQueueAndNothingElse(t *testing.T) {
	for _, tc := range revalidatorTemplates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseRevalidatorTemplate(t, tc.template)
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
			if want := []string{"RevalidateQueue.Arn"}; !slices.Equal(sendResources, want) {
				t.Errorf("edge user sends to %v, want only the revalidation queue's own ARN", sendResources)
			}
		})
	}
}

// TestEnsureRevalidatorArtifact_RefusesADigestMismatch is what the pin in
// revalidatorversion.go actually buys. The consumer holds an app's bypass token
// and signs requests at its origin, so bytes that are not the reviewed artifact
// must never reach a customer's account: bootstrap stops, uploads nothing, and
// names both digests so an operator can tell a moved release from a tampered
// download.
func TestEnsureRevalidatorArtifact_RefusesADigestMismatch(t *testing.T) {
	art, store, source := fixtureArtifactDeps([]byte("not the revalidator anyone reviewed"))

	code, err := ensureRevalidatorArtifact(context.Background(), art, "ocel-artifacts-test", fixtureRevalidatorPin())
	if err == nil {
		t.Fatal("a mismatched revalidator was accepted; it must refuse to deploy")
	}
	if code.present() {
		t.Errorf("a refused revalidator still produced code %+v", code)
	}
	if store.puts != 0 {
		t.Errorf("a refused revalidator was uploaded anyway (%d puts)", store.puts)
	}
	if want := revalidatorReleaseURL(fixtureRevalidatorPin().version); len(source.urls) != 1 || source.urls[0] != want {
		t.Errorf("fetched %v, want exactly [%s]", source.urls, want)
	}
	if !strings.Contains(err.Error(), fixtureRevalidatorPin().sha256) {
		t.Errorf("error does not name the required digest: %v", err)
	}
	if !strings.Contains(err.Error(), revalidatorLabel) {
		t.Errorf("error does not name which artifact refused: %v", err)
	}
}

// TestEnsureRevalidatorArtifact_UploadsAVerifiedArtifact is the same guarantee
// from the other side: the bytes that do hash to the pin are uploaded verbatim,
// under a key content-addressed on the digest, with the pin handed to S3 so the
// stored checksum is something a later bootstrap can verify presence against.
func TestEnsureRevalidatorArtifact_UploadsAVerifiedArtifact(t *testing.T) {
	art, store, _ := fixtureArtifactDeps(fixtureArtifact)

	code, err := ensureRevalidatorArtifact(context.Background(), art, "ocel-artifacts-test", fixtureRevalidatorPin())
	if err != nil {
		t.Fatalf("ensureRevalidatorArtifact: %v", err)
	}
	if !strings.Contains(code.key, fixtureDigest()) {
		t.Errorf("key %q is not content-addressed on the pinned digest", code.key)
	}
	if got := string(store.objects[code.key]); got != string(fixtureArtifact) {
		t.Errorf("uploaded %q, want the verified bytes verbatim", got)
	}
	want, err := fixtureRevalidatorPin().checksum(revalidatorLabel)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if len(store.putChecksums) != 1 || store.putChecksums[0] != want {
		t.Errorf("uploaded with checksums %v, want [%s]", store.putChecksums, want)
	}
}
