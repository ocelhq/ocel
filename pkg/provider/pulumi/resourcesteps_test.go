package pulumi

import (
	"slices"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

const (
	stackResourceURN = "urn:pulumi:prod::proj::pulumi:pulumi:Stack::proj-prod"
	bucketURN        = "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::assets"
	roleURN          = "urn:pulumi:prod::proj::aws:iam/role:Role::runner"
	awsRef           = "urn:pulumi:prod::proj::pulumi:providers:aws::default::0f6e"
)

func stepWithParent(op apitype.OpType, urn, resourceType, parent string) apitype.StepEventMetadata {
	return apitype.StepEventMetadata{
		Op:       op,
		URN:      urn,
		Type:     resourceType,
		Provider: awsRef,
		New:      &apitype.StepEventStateMetadata{URN: urn, Type: resourceType, Parent: parent},
	}
}

func preOf(m apitype.StepEventMetadata) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{ResourcePreEvent: &apitype.ResourcePreEvent{Metadata: m}}}
}

func outputsOf(m apitype.StepEventMetadata) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{ResOutputsEvent: &apitype.ResOutputsEvent{Metadata: m}}}
}

func failedOf(m apitype.StepEventMetadata) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{ResOpFailedEvent: &apitype.ResOpFailedEvent{Metadata: m}}}
}

func errorDiagnostic(urn, message string) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{DiagnosticEvent: &apitype.DiagnosticEvent{
		URN: urn, Severity: diagnosticSeverityError, Message: message,
	}}}
}

func stepSpanOf(t *testing.T, spans []sdktrace.ReadOnlySpan, urn string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if v, ok := tracingtest.Attributes(span)["pulumi.resource.urn"]; ok && v.AsString() == urn {
			return span
		}
	}
	t.Fatalf("no step span for %s", urn)
	return nil
}

func TestEachResourceStepIsASpanFromItsPreEventToItsOutcome(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)
	ctx, operation := tracer.Start(ctx, "provision stack")

	bucket := stepWithParent(apitype.OpUpdate, bucketURN, "aws:s3/bucket:Bucket", stackResourceURN)
	bucket.Diffs = []string{"tags", "versioning"}
	bucket.DetailedDiff = map[string]apitype.PropertyDiff{"tags.team": {Kind: apitype.DiffUpdate, InputDiff: true}}
	stack := stepWithParent(apitype.OpSame, stackResourceURN, stackResourceType, "")

	base := time.Unix(5000, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(stack), base)
	steps.consume(preOf(bucket), base.Add(time.Second))
	steps.consume(outputsOf(bucket), base.Add(4*time.Second))
	steps.consume(outputsOf(stack), base.Add(5*time.Second))
	operation.End()

	spans := recorded.Ended()
	got := stepSpanOf(t, spans, bucketURN)
	if got.Name() != "update resource" {
		t.Errorf("span name = %q, want update resource", got.Name())
	}
	if !got.StartTime().Equal(base.Add(time.Second)) || !got.EndTime().Equal(base.Add(4*time.Second)) {
		t.Fatalf("span ran %v to %v, want %v to %v", got.StartTime(), got.EndTime(), base.Add(time.Second), base.Add(4*time.Second))
	}
	attrs := tracingtest.Attributes(got)
	want := map[string]string{
		"pulumi.resource.type":     "aws:s3/bucket:Bucket",
		"pulumi.resource.provider": awsRef,
		"pulumi.resource.parent":   stackResourceURN,
		"pulumi.step.op":           "update",
	}
	for key, value := range want {
		if attrs[key].AsString() != value {
			t.Errorf("%s = %q, want %q", key, attrs[key].String(), value)
		}
	}
	if v := attrs["pulumi.step.diffs"]; !slices.Equal(v.AsStringSlice(), []string{"tags", "versioning"}) {
		t.Errorf("pulumi.step.diffs = %q, want [tags versioning]", v.AsStringSlice())
	}
	if v := attrs["pulumi.step.detailed_diff"]; !slices.Equal(v.AsStringSlice(), []string{"tags.team: update"}) {
		t.Errorf("pulumi.step.detailed_diff = %q, want [tags.team: update]", v.AsStringSlice())
	}

	stackSpan := stepSpanOf(t, spans, stackResourceURN)
	if stackSpan.Name() != "keep resource" {
		t.Errorf("the stack's span name = %q, want keep resource", stackSpan.Name())
	}
	if got.Parent().SpanID() != stackSpan.SpanContext().SpanID() {
		t.Errorf("the bucket's step is not nested under its parent resource's step")
	}
	if stackSpan.Parent().SpanID() != operation.SpanContext().SpanID() {
		t.Errorf("the stack's step is not nested under the operation")
	}
}

func TestAChangedResourceIsOneStepSpanNamingTheResourceAndItsActionUnderTheProgressKeys(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	bucket := stepWithParent(apitype.OpCreate, bucketURN, "aws:s3/bucket:Bucket", "")
	role := stepWithParent(apitype.OpSame, roleURN, "aws:iam/role:Role", "")
	base := time.Unix(5200, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(bucket), base)
	steps.consume(preOf(role), base)
	steps.consume(outputsOf(bucket), base.Add(2*time.Second))
	steps.consume(outputsOf(role), base.Add(2*time.Second))
	steps.close(base.Add(3 * time.Second))

	attrs := tracingtest.Attributes(stepSpanOf(t, recorded.Ended(), bucketURN))
	want := map[string]string{
		"ocel.span_name":       "create resource",
		"ocel.resource_type":   "aws:s3/bucket:Bucket",
		"ocel.resource_name":   "assets",
		"ocel.resource_action": "create",
	}
	for key, value := range want {
		if attrs[key].AsString() != value {
			t.Errorf("%s = %q, want %q", key, attrs[key].String(), value)
		}
	}
	if _, named := tracingtest.Attributes(stepSpanOf(t, recorded.Ended(), roleURN))["ocel.resource_action"]; named {
		t.Error("the unchanged role's step names a resource action, want only the changed bucket's to")
	}
	if len(steps.ops) != 1 || steps.ops[0].Name != "assets" || !steps.ops[0].Start.Equal(base) || !steps.ops[0].End.Equal(base.Add(2*time.Second)) {
		t.Errorf("ops = %+v, want the bucket's create alone, timed as its step span", steps.ops)
	}
}

func TestAnImportReplacementIsNamedAndReportedAsTheReplaceItPerforms(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	bucket := stepWithParent(apitype.OpImportReplacement, bucketURN, "aws:s3/bucket:Bucket", "")
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(bucket), time.Unix(5300, 0))
	steps.consume(outputsOf(bucket), time.Unix(5301, 0))

	if got := stepSpanOf(t, recorded.Ended(), bucketURN).Name(); got != "replace resource" {
		t.Errorf("span name = %q, want replace resource", got)
	}
	if len(steps.ops) != 1 || steps.ops[0].progressName() != "replace resource" {
		t.Errorf("ops = %+v, want one replace reported as replace resource", steps.ops)
	}
}

func TestARefreshedResourceEndsItsStepWhenTheEngineReportsTheRefreshOutcome(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	refresh := stepWithParent(apitype.OpRefresh, bucketURN, "aws:s3/bucket:Bucket", "")
	outcome := refresh
	outcome.Op = apitype.OpSame
	base := time.Unix(5400, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(refresh), base)
	steps.consume(outputsOf(outcome), base.Add(time.Second))
	steps.close(base.Add(time.Minute))

	got := stepSpanOf(t, recorded.Ended(), bucketURN)
	if got.Status().Code == codes.Error || !got.EndTime().Equal(base.Add(time.Second)) {
		t.Errorf("refresh step ended %v with %v, want it ended at its outcome %v without an error", got.EndTime(), got.Status(), base.Add(time.Second))
	}
}

func TestAFailedStepIsAnErrorSpanCarryingTheEngineDiagnosticWithSecretsMasked(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	role := stepWithParent(apitype.OpCreate, roleURN, "aws:iam/role:Role", "")
	base := time.Unix(6000, 0)
	steps := newResourceSteps(ctx, []string{"hunter2"})
	steps.consume(preOf(role), base)
	steps.consume(errorDiagnostic(roleURN, "creating role: password hunter2 rejected"), base.Add(time.Second))
	steps.consume(failedOf(role), base.Add(2*time.Second))

	got := stepSpanOf(t, recorded.Ended(), roleURN)
	if got.Status().Code != codes.Error {
		t.Fatalf("status = %v, want Error", got.Status().Code)
	}
	if want := "creating role: password " + tracing.Redacted + " rejected"; got.Status().Description != want {
		t.Errorf("status message = %q, want %q", got.Status().Description, want)
	}
	if !got.EndTime().Equal(base.Add(2 * time.Second)) {
		t.Errorf("span ended at %v, want %v", got.EndTime(), base.Add(2*time.Second))
	}
}

func TestTheOperationSpanCountsEveryResourceStepTheEngineFinished(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)
	ctx, operation := tracer.Start(ctx, "provision stack")

	bucket := stepWithParent(apitype.OpCreate, bucketURN, "aws:s3/bucket:Bucket", stackResourceURN)
	role := stepWithParent(apitype.OpSame, roleURN, "aws:iam/role:Role", stackResourceURN)
	base := time.Unix(5500, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(bucket), base)
	steps.consume(outputsOf(bucket), base.Add(time.Second))
	steps.consume(outputsOf(role), base.Add(time.Second))
	steps.consume(preOf(stepWithParent(apitype.OpCreate, awsRef, "pulumi:providers:aws", "")), base.Add(time.Second))
	steps.close(base.Add(2 * time.Second))
	operation.End()

	got := tracingtest.SpanNamed(t, recorded.Ended(), "provision stack")
	if v := tracingtest.Attributes(got)["ocel.resource_count"]; v.AsInt64() != 2 {
		t.Errorf("ocel.resource_count = %d, want the 2 steps the engine reported an outcome for", v.AsInt64())
	}
}

func TestAStepTheEngineNeverFinishedEndsAsAnErrorWhenTheStreamCloses(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	base := time.Unix(7000, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(preOf(stepWithParent(apitype.OpCreate, roleURN, "aws:iam/role:Role", "")), base)
	steps.close(base.Add(3 * time.Second))

	got := stepSpanOf(t, recorded.Ended(), roleURN)
	if got.Status().Code != codes.Error || !got.EndTime().Equal(base.Add(3*time.Second)) {
		t.Fatalf("unfinished step ended %v with %v, want an error at %v", got.EndTime(), got.Status().Code, base.Add(3*time.Second))
	}
}

func TestAPlansEngineEventsBecomeStepSpans(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	engineEvents := make(chan events.EngineEvent, 2)
	drained := drainRows(engineEvents, newResourceSteps(ctx, nil))
	bucket := stepWithParent(apitype.OpCreate, bucketURN, "aws:s3/bucket:Bucket", "")
	engineEvents <- preOf(bucket)
	engineEvents <- outputsOf(bucket)
	close(engineEvents)
	if _, err := awaitRows(drained, time.Second); err != nil {
		t.Fatal(err)
	}

	stepSpanOf(t, recorded.Ended(), bucketURN)
}

func TestAPluginDownloadIsASpanFromItsFirstProgressToDone(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)

	progressOf := func(done bool) events.EngineEvent {
		return events.EngineEvent{EngineEvent: apitype.EngineEvent{ProgressEvent: &apitype.ProgressEvent{
			Type: apitype.PluginDownload, ID: "aws-6.0.0", Message: "Downloading plugin aws v6.0.0", Completed: 10, Total: 10, Done: done,
		}}}
	}
	base := time.Unix(8000, 0)
	steps := newResourceSteps(ctx, nil)
	steps.consume(progressOf(false), base)
	steps.consume(progressOf(true), base.Add(2*time.Second))

	got := tracingtest.SpanNamed(t, recorded.Ended(), "download plugin")
	if !got.StartTime().Equal(base) || !got.EndTime().Equal(base.Add(2*time.Second)) {
		t.Fatalf("download ran %v to %v, want %v to %v", got.StartTime(), got.EndTime(), base, base.Add(2*time.Second))
	}
}
