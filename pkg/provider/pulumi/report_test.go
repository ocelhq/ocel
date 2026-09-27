package pulumi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordedSpan struct {
	name  string
	err   error
	attrs []edge.Attr
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

func (r *fakeProgress) Span(name string, _, _ time.Time, err error, attrs ...edge.Attr) {
	r.spans = append(r.spans, recordedSpan{name: name, err: err, attrs: attrs})
}

func TestTheBatchSpanHasNoResourceIdentityAndTheSlowOpDoes(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(6000, 0)
	reportTrace(progress, engineTrace{
		ResourceCount: 2,
		Start:         start,
		End:           start.Add(5 * time.Second),
		Failed:        true,
		Ops: []resourceOp{{
			Op:     apitype.OpCreate,
			Type:   "aws:s3/bucket:Bucket",
			Name:   "my-bucket",
			Start:  start,
			End:    start.Add(5 * time.Second),
			Failed: true,
		}},
	}, nil)

	if len(progress.spans) != 2 {
		t.Fatalf("got %d spans, want 2 (batch + the failed op)", len(progress.spans))
	}

	batch := progress.spans[0]
	if batch.name != engineBatchSpanName {
		t.Fatalf("spans[0].name = %q, want the batch span name", batch.name)
	}
	for _, a := range batch.attrs {
		if a.Key == provider.AttrKeyResourceType || a.Key == provider.AttrKeyResourceName {
			t.Errorf("batch span has resource identity attr %+v; it covers many resources", a)
		}
	}

	var sawType, sawName bool
	for _, a := range progress.spans[1].attrs {
		switch a.Key {
		case provider.AttrKeyResourceType:
			sawType = true
			if a.Value != "aws:s3/bucket:Bucket" {
				t.Errorf("RESOURCE_TYPE = %q, want the type token", a.Value)
			}
		case provider.AttrKeyResourceName:
			sawName = true
			if a.Value != "my-bucket" {
				t.Errorf("RESOURCE_NAME = %q, want the logical name", a.Value)
			}
			if strings.Contains(a.Value, "urn:pulumi") {
				t.Fatal("RESOURCE_NAME contained the raw URN")
			}
		}
	}
	if !sawType {
		t.Error("slow-op span is missing ATTRIBUTE_KEY_RESOURCE_TYPE")
	}
	if !sawName {
		t.Error("slow-op span is missing ATTRIBUTE_KEY_RESOURCE_NAME")
	}
}

func TestASlowOpWhoseURNDidNotParseHasNoResourceIdentity(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(7000, 0)
	reportTrace(progress, engineTrace{
		ResourceCount: 1,
		Start:         start,
		End:           start.Add(time.Second),
		Failed:        true,
		Ops: []resourceOp{
			{Op: apitype.OpCreate, Start: start, End: start.Add(time.Second), Failed: true},
		},
	}, nil)

	if len(progress.spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(progress.spans))
	}
	for _, a := range progress.spans[1].attrs {
		if a.Key == provider.AttrKeyResourceType || a.Key == provider.AttrKeyResourceName {
			t.Errorf("slow-op span has resource identity attr %+v despite an unparseable URN", a)
		}
	}
}

func TestARunThatFailedBeforeTouchingAResourceStillLeavesASpan(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(8000, 0)
	reportTrace(progress, engineTrace{Start: start, End: start.Add(time.Second)}, errors.New("plugin failed to start"))

	if len(progress.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(progress.spans))
	}
	if progress.spans[0].err == nil {
		t.Error("batch span not recorded as failed")
	}
}

func TestAQuietSuccessfulRunSaysNothing(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	reportTrace(progress, engineTrace{}, nil)

	if len(progress.spans) != 0 {
		t.Fatalf("got %d spans, want 0: nothing happened and nothing failed", len(progress.spans))
	}
}

func TestATraceThatNeverArrivesReturnsWithinItsGrace(t *testing.T) {
	t.Parallel()

	result := make(chan engineTrace)
	done := make(chan engineTrace, 1)
	go func() { done <- awaitTrace(result, 20*time.Millisecond) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitTrace did not return within its grace period")
	}
}

func TestAnEventStreamThatIsNeverClosedLeavesTheRunToFinish(t *testing.T) {
	t.Parallel()

	engineEvents := make(chan events.EngineEvent, 4)
	engineEvents <- events.EngineEvent{}

	trace := awaitTrace(drainTrace(engineEvents, 0, nil), 50*time.Millisecond)
	if !reflect.DeepEqual(trace, engineTrace{}) {
		t.Errorf("got %+v, want a zero-value trace: the channel is unclosed so the builder goroutine never sent a result", trace)
	}
}

func TestTheEngineLogIsForwardedToDebugALineAtATime(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	lines := engineLines(progress)
	if _, err := lines.Write([]byte("creating bucket\r\nupdating role\npart")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"creating bucket", "updating role"}; !reflect.DeepEqual(progress.debugs, want) {
		t.Fatalf("forwarded %q to debug, want %q", progress.debugs, want)
	}
	lines.Flush()
	if len(progress.debugs) != 3 || progress.debugs[2] != "part" {
		t.Errorf("forwarded %q to debug, want the trailing partial line flushed", progress.debugs)
	}
	if len(progress.details) != 0 {
		t.Errorf("the engine's own output reached Detail: %q", progress.details)
	}
}

func reported(secrets []string, engineEvents ...events.EngineEvent) *fakeProgress {
	collector := newTraceCollector(time.Hour, secrets)
	at := time.Unix(9000, 0)
	for _, ev := range engineEvents {
		collector.consume(ev, at)
		at = at.Add(time.Second)
	}
	progress := &fakeProgress{}
	reportTrace(progress, collector.result(), nil)
	return progress
}

func attrValue(span recordedSpan, key string) string {
	for _, a := range span.attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func changes(progress *fakeProgress) []string {
	var changed []string
	for _, span := range progress.spans {
		if action := attrValue(span, provider.AttrKeyResourceAction); action != "" {
			changed = append(changed, action+" "+attrValue(span, provider.AttrKeyResourceType)+" "+attrValue(span, provider.AttrKeyResourceName))
		}
	}
	return changed
}

func urnOf(typ, name string) string { return "urn:pulumi:prod::proj::" + typ + "::" + name }

func TestEveryResourceTheRunChangedIsReportedWithItsActionHoweverFastItWas(t *testing.T) {
	t.Parallel()

	bucket, role, old := urnOf("aws:s3/bucket:Bucket", "assets"), urnOf("aws:iam/role:Role", "api"), urnOf("aws:sqs/queue:Queue", "old")
	progress := reported(nil,
		preEvent(bucket, "aws:s3/bucket:Bucket"), outputsEvent(bucket, "aws:s3/bucket:Bucket", apitype.OpCreate),
		preEvent(role, "aws:iam/role:Role"), outputsEvent(role, "aws:iam/role:Role", apitype.OpUpdate),
		preEvent(old, "aws:sqs/queue:Queue"), outputsEvent(old, "aws:sqs/queue:Queue", apitype.OpDelete),
	)

	want := []string{"create aws:s3/bucket:Bucket assets", "update aws:iam/role:Role api", "delete aws:sqs/queue:Queue old"}
	if got := changes(progress); !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want %q", got, want)
	}
}

func TestAnUnchangedResourceAndTheStackItselfAreNeverReportedAsChanges(t *testing.T) {
	t.Parallel()

	kept, stack, bucket := urnOf("aws:iam/role:Role", "api"), urnOf(stackResourceType, "proj-prod"), urnOf("aws:s3/bucket:Bucket", "assets")
	progress := reported(nil,
		preEvent(stack, stackResourceType), outputsEvent(stack, stackResourceType, apitype.OpCreate),
		preEvent(kept, "aws:iam/role:Role"), outputsEvent(kept, "aws:iam/role:Role", apitype.OpSame),
		preEvent(bucket, "aws:s3/bucket:Bucket"), outputsEvent(bucket, "aws:s3/bucket:Bucket", apitype.OpCreate),
	)

	if got, want := changes(progress), []string{"create aws:s3/bucket:Bucket assets"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want only %q", got, want)
	}
}

func TestAReplacedResourceIsReportedOnceAsReplacedAndAFailedReplacementAsAFailedReplace(t *testing.T) {
	t.Parallel()

	fn, db := urnOf("aws:lambda/function:Function", "api"), urnOf("aws:rds/instance:Instance", "main")
	progress := reported(nil,
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpCreateReplacement),
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpReplace),
		preEvent(fn, "aws:lambda/function:Function"), outputsEvent(fn, "aws:lambda/function:Function", apitype.OpDeleteReplaced),
		preEvent(db, "aws:rds/instance:Instance"), failedEvent(db, "aws:rds/instance:Instance", apitype.OpCreateReplacement),
	)

	want := []string{"replace aws:lambda/function:Function api", "replace aws:rds/instance:Instance main"}
	if got := changes(progress); !reflect.DeepEqual(got, want) {
		t.Fatalf("reported changes %q, want %q", got, want)
	}
	if failed := progress.spans[len(progress.spans)-1]; failed.err == nil {
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
	progress := reported(nil,
		preEvent(assets, "aws:s3/bucket:Bucket"),
		diagnostic(assets, "warning", "the bucket's ACL is deprecated"),
		outputsEvent(assets, "aws:s3/bucket:Bucket", apitype.OpCreate),
		preEvent(logs, "aws:s3/bucket:Bucket"),
		diagnostic(logs, "error", "<{%reset%}>creating S3 Bucket (logs): BucketAlreadyExists\n\tstatus code: 409<{%reset%}>\n"),
		failedEvent(logs, "aws:s3/bucket:Bucket", apitype.OpCreate),
	)

	want := []string{"logs (aws:s3/bucket:Bucket): creating S3 Bucket (logs): BucketAlreadyExists status code: 409"}
	if !reflect.DeepEqual(progress.errors, want) {
		t.Fatalf("reported errors %q, want %q", progress.errors, want)
	}
	if len(progress.debugs) != 0 || len(progress.warned) != 0 {
		t.Errorf("reported %q as debug and %q as warnings, want the failure's diagnostic as an error alone", progress.debugs, progress.warned)
	}
}

func TestAFailedResourcesDiagnosticNeverCarriesASecretTheStackIsConfiguredWith(t *testing.T) {
	t.Parallel()

	db := urnOf("aws:rds/instance:Instance", "main")
	secret := "hunter2-p4ssw0rd"
	progress := reported(secretValues(auto.ConfigMap{
		"aws:region":      {Value: "us-east-1"},
		"app:dbPassword":  {Value: secret, Secret: true},
		"app:emptySecret": {Value: "", Secret: true},
	}),
		preEvent(db, "aws:rds/instance:Instance"),
		diagnostic(db, "error", "creating RDS DB Instance: InvalidParameterValue: MasterUserPassword "+secret+" is not valid in us-east-1"),
		failedEvent(db, "aws:rds/instance:Instance", apitype.OpCreate),
	)

	want := []string{"main (aws:rds/instance:Instance): creating RDS DB Instance: InvalidParameterValue: MasterUserPassword [secret] is not valid in us-east-1"}
	if !reflect.DeepEqual(progress.errors, want) {
		t.Fatalf("reported errors %q, want %q", progress.errors, want)
	}
}
