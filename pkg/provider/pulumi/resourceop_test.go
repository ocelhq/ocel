package pulumi

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func preEvent(urn, resourceType string) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: resourceType, Op: apitype.OpCreate},
		},
	}}
}

func outputsEvent(urn, resourceType string, op apitype.OpType) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResOutputsEvent: &apitype.ResOutputsEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: resourceType, Op: op},
		},
	}}
}

func failedEvent(urn, resourceType string, op apitype.OpType) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResOpFailedEvent: &apitype.ResOpFailedEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: resourceType, Op: op},
		},
	}}
}

const testURN = "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::my-bucket"

func consumed(steps *resourceSteps, engineEvents ...events.EngineEvent) []resourceOp {
	at := time.Unix(9000, 0)
	for _, event := range engineEvents {
		steps.consume(event, at)
		at = at.Add(time.Second)
	}
	return steps.ops
}

func TestAFailedOpIsRecordedFromItsPreEventToItsFailure(t *testing.T) {
	t.Parallel()

	steps := newResourceSteps(context.Background(), nil)
	base := time.Unix(2000, 0)
	steps.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	steps.consume(failedEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(5*time.Second))

	if len(steps.ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(steps.ops))
	}
	s := steps.ops[0]
	if !s.Failed {
		t.Error("ops[0].Failed = false, want true")
	}
	if !s.Start.Equal(base) || !s.End.Equal(base.Add(5*time.Second)) {
		t.Errorf("ops[0] Start/End = %v/%v, want %v/%v", s.Start, s.End, base, base.Add(5*time.Second))
	}
}

func TestAnUnchangedOpIsNeverRecordedHoweverLongItTook(t *testing.T) {
	t.Parallel()

	steps := newResourceSteps(context.Background(), nil)
	base := time.Unix(3000, 0)
	steps.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	steps.consume(outputsEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpSame), base.Add(10*time.Minute))

	if len(steps.ops) != 0 {
		t.Fatalf("ops = %+v, want none: its step span carries how long it took", steps.ops)
	}
}

func TestARecordedOpNeverRetainsTheURN(t *testing.T) {
	t.Parallel()

	ops := consumed(newResourceSteps(context.Background(), nil),
		preEvent(testURN, "aws:s3/bucket:Bucket"),
		failedEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate),
	)
	if len(ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(ops))
	}
	s := ops[0]
	if s.Type != "aws:s3/bucket:Bucket" {
		t.Errorf("Type = %q, want the type token", s.Type)
	}
	if s.Name != "my-bucket" {
		t.Errorf("Name = %q, want the URN's logical name component", s.Name)
	}
	if strings.Contains(s.Type, "prod") || strings.Contains(s.Type, "proj") {
		t.Fatalf("Type leaked the URN's stack or project: %q", s.Type)
	}
	if strings.Contains(s.Name, "prod") || strings.Contains(s.Name, "proj") {
		t.Fatalf("Name leaked the URN's stack or project: %q", s.Name)
	}
}

func TestResourceNameFromURNParsesDefensively(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		urn  string
		want string
	}{
		{"well-formed", testURN, "my-bucket"},
		{"empty", "", ""},
		{"not a urn at all", "not-a-urn", ""},
		{"missing the name component", "urn:pulumi:prod::proj::aws:s3/bucket:Bucket", ""},
		{"wrong namespace", "urn:notpulumi:prod::proj::aws:s3/bucket:Bucket::my-bucket", ""},
		{"name embeds the delimiter", "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::weird::name", "weird::name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resourceNameFromURN(tc.urn); got != tc.want {
				t.Errorf("resourceNameFromURN(%q) = %q, want %q", tc.urn, got, tc.want)
			}
		})
	}
}

func TestCapIdentifierBoundsLength(t *testing.T) {
	t.Parallel()

	short := "aws:s3/bucket:Bucket"
	if got := capIdentifier(short); got != short {
		t.Errorf("capIdentifier(%q) = %q, want unchanged", short, got)
	}

	long := strings.Repeat("x", maxResourceIdentifierLen*2)
	got := capIdentifier(long)
	if len(got) != maxResourceIdentifierLen {
		t.Fatalf("capIdentifier(long) length = %d, want %d", len(got), maxResourceIdentifierLen)
	}

	multibyte := strings.Repeat("é", maxResourceIdentifierLen)
	got = capIdentifier(multibyte)
	if !utf8.ValidString(got) {
		t.Fatalf("capIdentifier(multibyte) = %q, not valid UTF-8", got)
	}
	if len(got) > maxResourceIdentifierLen {
		t.Fatalf("capIdentifier(multibyte) length = %d, want <= %d", len(got), maxResourceIdentifierLen)
	}
}

func TestAResourceOpsNameIsBoundedNeverDynamic(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for op, action := range performedActions {
		name := resourceOp{Action: action}.progressName()
		if name != stepName(op) {
			t.Errorf("%s reports as %q but its step span is %q, want one name for both", op, name, stepName(op))
		}
		seen[name] = true
	}
	if name := (resourceOp{Action: provider.ActionCreate, Failed: true}).progressName(); name != "resource operation failed" {
		t.Errorf("a failed create reports as %q, want the fixed failure string", name)
	}
	if len(seen) != 4 {
		t.Errorf("changing ops report under %d names, want the 4 actions'", len(seen))
	}
}

type recordedSpan struct {
	name  string
	err   error
	attrs []progress.Attr
}

type fakeProgress struct {
	said    []string
	warned  []string
	errors  []string
	details []string
	debugs  []string
	spans   []recordedSpan
}

func (r *fakeProgress) Say(message string) { r.said = append(r.said, message) }

func (r *fakeProgress) Warn(message string) { r.warned = append(r.warned, message) }

func (r *fakeProgress) Error(message string) { r.errors = append(r.errors, message) }

func (r *fakeProgress) Detail(message string) { r.details = append(r.details, message) }

func (r *fakeProgress) Debug(line string) { r.debugs = append(r.debugs, line) }

func (r *fakeProgress) Span(name string, _, _ time.Time, err error, attrs ...progress.Attr) {
	r.spans = append(r.spans, recordedSpan{name: name, err: err, attrs: attrs})
}

func TestAFailedOpIsOneSpanNamingItsResource(t *testing.T) {
	t.Parallel()

	recorded := &fakeProgress{}
	start := time.Unix(6000, 0)
	reportResourceOps(recorded, []resourceOp{{
		Action: provider.ActionCreate,
		Type:   "aws:s3/bucket:Bucket",
		Name:   "my-bucket",
		Start:  start,
		End:    start.Add(5 * time.Second),
		Failed: true,
	}})

	if len(recorded.spans) != 1 {
		t.Fatalf("got %d spans, want the failed op's alone", len(recorded.spans))
	}
	got := recorded.spans[0]
	if got.err == nil {
		t.Error("the failed op was reported as a success")
	}
	if v := attrValue(got, progress.AttrKeyResourceType); v != "aws:s3/bucket:Bucket" {
		t.Errorf("RESOURCE_TYPE = %q, want the type token", v)
	}
	if v := attrValue(got, progress.AttrKeyResourceName); v != "my-bucket" {
		t.Errorf("RESOURCE_NAME = %q, want the logical name", v)
	}
}

func TestEveryReportedOpIsMarkedAsASpanTheProviderRecordsItself(t *testing.T) {
	t.Parallel()

	bucket := urnOf("aws:s3/bucket:Bucket", "assets")
	recorded := reported(nil,
		preEvent(bucket, "aws:s3/bucket:Bucket"), outputsEvent(bucket, "aws:s3/bucket:Bucket", apitype.OpCreate),
		preEvent(testURN, "aws:s3/bucket:Bucket"), failedEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpUpdate),
	)

	if len(recorded.spans) != 2 {
		t.Fatalf("got %d spans, want the create and the failed update", len(recorded.spans))
	}
	for _, span := range recorded.spans {
		if attrValue(span, progress.AttrKeyRecordedByProvider) != "true" {
			t.Errorf("span %q is not marked recorded by the provider, so the run's trace would hold it twice", span.name)
		}
	}
}

func TestAnOpWhoseURNDidNotParseHasNoResourceIdentity(t *testing.T) {
	t.Parallel()

	recorded := &fakeProgress{}
	start := time.Unix(7000, 0)
	reportResourceOps(recorded, []resourceOp{{Action: provider.ActionCreate, Start: start, End: start.Add(time.Second), Failed: true}})

	if len(recorded.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(recorded.spans))
	}
	for _, a := range recorded.spans[0].attrs {
		if a.Key == progress.AttrKeyResourceType || a.Key == progress.AttrKeyResourceName {
			t.Errorf("span has resource identity attr %+v despite an unparseable URN", a)
		}
	}
}

func TestAQuietSuccessfulRunSaysNothing(t *testing.T) {
	t.Parallel()

	recorded := &fakeProgress{}
	reportResourceOps(recorded, nil)

	if len(recorded.spans) != 0 {
		t.Fatalf("got %d spans, want 0: nothing happened and nothing failed", len(recorded.spans))
	}
}

func TestATraceThatNeverArrivesReturnsWithinItsGrace(t *testing.T) {
	t.Parallel()

	result := make(chan []resourceOp)
	done := make(chan []resourceOp, 1)
	go func() { done <- awaitResourceOps(result, 20*time.Millisecond) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitResourceOps did not return within its grace period")
	}
}

func TestAnEventStreamThatIsNeverClosedLeavesTheRunToFinish(t *testing.T) {
	t.Parallel()

	engineEvents := make(chan events.EngineEvent, 4)
	engineEvents <- events.EngineEvent{}

	ops := awaitResourceOps(drainResourceOps(engineEvents, newResourceSteps(context.Background(), nil)), 50*time.Millisecond)
	if ops != nil {
		t.Errorf("got %+v, want nothing: the channel is unclosed so the builder goroutine never sent a result", ops)
	}
}

func TestTheEngineLogIsForwardedToDebugALineAtATime(t *testing.T) {
	t.Parallel()

	recorded := &fakeProgress{}
	lines := engineLines(recorded, nil)
	if _, err := lines.Write([]byte("creating bucket\r\nupdating role\npart")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"creating bucket", "updating role"}; !reflect.DeepEqual(recorded.debugs, want) {
		t.Fatalf("forwarded %q to debug, want %q", recorded.debugs, want)
	}
	lines.Flush()
	if len(recorded.debugs) != 3 || recorded.debugs[2] != "part" {
		t.Errorf("forwarded %q to debug, want the trailing partial line flushed", recorded.debugs)
	}
	if len(recorded.details) != 0 {
		t.Errorf("the engine's own output reached Detail: %q", recorded.details)
	}
}

func TestASecretTheEnginePrintsReachesDebugMasked(t *testing.T) {
	t.Parallel()

	recorded := &fakeProgress{}
	setup := WorkspaceSpec{Secrets: []string{"origin-secret-value"}}
	err := runReportingResourceOperations(context.Background(), setup, recorded, func(engineEvents chan<- events.EngineEvent, lines *lineLog) error {
		close(engineEvents)
		_, err := lines.Write([]byte("~ header: origin-secret-value\nunmasked tail origin-secret-value"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded.debugs) != 2 {
		t.Fatalf("forwarded %q to debug, want both lines", recorded.debugs)
	}
	for _, line := range recorded.debugs {
		if strings.Contains(line, "origin-secret-value") {
			t.Errorf("forwarded %q to debug, want the secret masked", line)
		}
	}
}

func reported(secrets []string, engineEvents ...events.EngineEvent) *fakeProgress {
	recorded := &fakeProgress{}
	reportResourceOps(recorded, consumed(newResourceSteps(context.Background(), secrets), engineEvents...))
	return recorded
}

func attrValue(span recordedSpan, key progress.AttrKey) string {
	for _, a := range span.attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func changes(recorded *fakeProgress) []string {
	var changed []string
	for _, span := range recorded.spans {
		if action := attrValue(span, progress.AttrKeyResourceAction); action != "" {
			changed = append(changed, action+" "+attrValue(span, progress.AttrKeyResourceType)+" "+attrValue(span, progress.AttrKeyResourceName))
		}
	}
	return changed
}

func urnOf(resourceType, name string) string {
	return "urn:pulumi:prod::proj::" + resourceType + "::" + name
}

func TestEveryResourceTheRunChangedIsReportedWithItsActionHoweverFastItWas(t *testing.T) {
	t.Parallel()

	bucket, role, old := urnOf("aws:s3/bucket:Bucket", "assets"), urnOf("aws:iam/role:Role", "api"), urnOf("aws:sqs/queue:Queue", "old")
	recorded := reported(nil,
		preEvent(bucket, "aws:s3/bucket:Bucket"), outputsEvent(bucket, "aws:s3/bucket:Bucket", apitype.OpCreate),
		preEvent(role, "aws:iam/role:Role"), outputsEvent(role, "aws:iam/role:Role", apitype.OpUpdate),
		preEvent(old, "aws:sqs/queue:Queue"), outputsEvent(old, "aws:sqs/queue:Queue", apitype.OpDelete),
	)

	want := []string{"create aws:s3/bucket:Bucket assets", "update aws:iam/role:Role api", "delete aws:sqs/queue:Queue old"}
	if got := changes(recorded); !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want %q", got, want)
	}
}

func TestAnUnchangedResourceAndTheStackItselfAreNeverReportedAsChanges(t *testing.T) {
	t.Parallel()

	kept, stack, bucket := urnOf("aws:iam/role:Role", "api"), urnOf(stackResourceType, "proj-prod"), urnOf("aws:s3/bucket:Bucket", "assets")
	recorded := reported(nil,
		preEvent(stack, stackResourceType), outputsEvent(stack, stackResourceType, apitype.OpCreate),
		preEvent(kept, "aws:iam/role:Role"), outputsEvent(kept, "aws:iam/role:Role", apitype.OpSame),
		preEvent(bucket, "aws:s3/bucket:Bucket"), outputsEvent(bucket, "aws:s3/bucket:Bucket", apitype.OpCreate),
	)

	if got, want := changes(recorded), []string{"create aws:s3/bucket:Bucket assets"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want only %q", got, want)
	}
}

func TestAReplacedResourceIsReportedOnceAsReplacedAndAFailedReplacementAsAFailedReplace(t *testing.T) {
	t.Parallel()

	fn, db := urnOf("aws:lambda/function:Function", "api"), urnOf("aws:rds/instance:Instance", "main")
	recorded := reported(nil,
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpCreateReplacement),
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpReplace),
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpDeleteReplaced),
		preEvent(db, "aws:rds/instance:Instance"), failedEvent(db, "aws:rds/instance:Instance", apitype.OpCreateReplacement),
	)

	want := []string{"replace aws:lambda/function:Function api", "replace aws:rds/instance:Instance main"}
	if got := changes(recorded); !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want %q", got, want)
	}
	if failed := recorded.spans[len(recorded.spans)-1]; failed.err == nil {
		t.Error("the failed replacement was reported as a success")
	}
}

func diagnostic(urn, severity, message string) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{URN: urn, Prefix: severity + ": ", Message: message, Color: "raw", Severity: severity},
	}}
}

func TestAFailedResourcesErrorDiagnosticIsReportedAsAnErrorNamingTheResource(t *testing.T) {
	t.Parallel()

	logs, assets := urnOf("aws:s3/bucket:Bucket", "logs"), urnOf("aws:s3/bucket:Bucket", "assets")
	recorded := reported(nil,
		preEvent(assets, "aws:s3/bucket:Bucket"),
		diagnostic(assets, "warning", "the bucket's ACL is deprecated"),
		outputsEvent(assets, "aws:s3/bucket:Bucket", apitype.OpCreate),
		preEvent(logs, "aws:s3/bucket:Bucket"),
		diagnostic(logs, "error", "<{%reset%}>creating S3 Bucket (logs): BucketAlreadyExists\n\tstatus code: 409<{%reset%}>\n"),
		failedEvent(logs, "aws:s3/bucket:Bucket", apitype.OpCreate),
	)

	want := []string{"logs (aws:s3/bucket:Bucket): creating S3 Bucket (logs): BucketAlreadyExists status code: 409"}
	if !reflect.DeepEqual(recorded.errors, want) {
		t.Fatalf("reported errors %q, want %q", recorded.errors, want)
	}
	if len(recorded.debugs) != 0 || len(recorded.warned) != 0 {
		t.Errorf("reported %q as debug and %q as warnings, want the failure's diagnostic as an error alone", recorded.debugs, recorded.warned)
	}
}

func TestAFailedResourcesDiagnosticNeverCarriesASecretTheRunKnows(t *testing.T) {
	t.Parallel()

	db := urnOf("aws:rds/instance:Instance", "main")
	secret := "hunter2-p4ssw0rd"
	recorded := reported([]string{secret},
		preEvent(db, "aws:rds/instance:Instance"),
		diagnostic(db, "error", "creating RDS DB Instance: InvalidParameterValue: MasterUserPassword "+secret+" is not valid in us-east-1"),
		failedEvent(db, "aws:rds/instance:Instance", apitype.OpCreate),
	)

	want := []string{"main (aws:rds/instance:Instance): creating RDS DB Instance: InvalidParameterValue: MasterUserPassword [secret] is not valid in us-east-1"}
	if !reflect.DeepEqual(recorded.errors, want) {
		t.Fatalf("reported errors %q, want %q", recorded.errors, want)
	}
}
