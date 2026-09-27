package runui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func (c *clock) pass(d time.Duration) { c.at = c.at.Add(d) }

func groupedRun(t *testing.T, present Presentation) (*events.Run, *bytes.Buffer, *clock) {
	t.Helper()
	var out bytes.Buffer
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, context.Background(), c.now, newGroupedSink(&out, present, nil))
	return run, &out, c
}

func TestAPhaseLevelLinePrintsTheMomentItLands(t *testing.T) {
	t.Parallel()

	run, out, _ := groupedRun(t, Presentation{})
	run.Phase(progressv1.Phase_PHASE_CHECK).Warn("the zone example.com has no Workers entitlement")

	want := "WARN  [check] the zone example.com has no Workers entitlement\n"
	if got := out.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestAUnitsOutputPrintsWithItsHeaderWhenTheUnitEndsIndentedFourSpaces(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "built 12 routes")
	w := web.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT)
	if _, err := w.Write([]byte("> next build\nCompiled successfully\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("printed before the unit ended: %q", got)
	}

	c.pass(34 * time.Second)
	web.End(nil)
	build.Say("every app is built")

	want := "INFO  [build] ✓ web: built 12 routes in 34s\n" +
		"\n" +
		"    > next build\n" +
		"    Compiled successfully\n" +
		"\n" +
		"INFO  [build] every app is built\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func providerEvent(span byte, subject string, op *progressv1.OperationEvent) *progressv1.OperationEvent {
	op.Phase, op.Subject, op.SpanId = progressv1.Phase_PHASE_DEPLOY, subject, []byte{span, 0, 0, 0, 0, 0, 0, 1}
	return op
}

func providerStarted(span byte, subject, message string, at time.Time) *progressv1.OperationEvent {
	return providerEvent(span, subject, &progressv1.OperationEvent{
		TimeUnixNano: at.UnixNano(),
		Message:      message,
		Body:         &progressv1.OperationEvent_Started{Started: &progressv1.Started{}},
	})
}

func providerOutput(span byte, subject, text string) *progressv1.OperationEvent {
	return providerEvent(span, subject, &progressv1.OperationEvent{
		Message: text,
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDOUT}},
	})
}

func providerEnded(span byte, subject string, status progressv1.SpanStatus, start, at time.Time) *progressv1.OperationEvent {
	level := progressv1.Level_LEVEL_INFO
	if status == progressv1.SpanStatus_SPAN_STATUS_ERROR {
		level = progressv1.Level_LEVEL_ERROR
	}
	return providerEvent(span, subject, &progressv1.OperationEvent{
		TimeUnixNano: at.UnixNano(),
		Level:        level,
		Body:         &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{Status: status, StartTimeUnixNano: start.UnixNano()}},
	})
}

func TestTwoParallelUnitsNeverInterleaveAndTheFirstToEndPrintsFirst(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	start := c.now()
	deploy.Forward(providerStarted(1, "web", "deployed 12 resources", start))
	deploy.Forward(providerStarted(2, "api", "deployed 9 resources", start))
	deploy.Forward(providerOutput(1, "web", "web line 1"))
	deploy.Forward(providerOutput(2, "api", "api line 1"))
	deploy.Forward(providerOutput(1, "web", "web line 2"))
	deploy.Forward(providerOutput(2, "api", "api line 2"))
	deploy.Forward(providerEnded(2, "api", progressv1.SpanStatus_SPAN_STATUS_OK, start, start.Add(5*time.Second)))
	deploy.Forward(providerEnded(1, "web", progressv1.SpanStatus_SPAN_STATUS_OK, start, start.Add(9*time.Second)))

	want := "INFO  [deploy] ✓ api: deployed 9 resources in 5s\n" +
		"\n" +
		"    api line 1\n" +
		"    api line 2\n" +
		"\n" +
		"INFO  [deploy] ✓ web: deployed 12 resources in 9s\n" +
		"\n" +
		"    web line 1\n" +
		"    web line 2\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestOnceAUnitHasFailedALaterSuccessfulUnitShowsOnlyItsHeader(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	api := build.Unit("api", "Building api")
	output(t, web, "web compiled")
	output(t, api, "api: missing module")
	c.pass(3 * time.Second)
	api.End(errors.New("npm run build exited with status 1"))
	c.pass(2 * time.Second)
	web.End(nil)

	want := "ERROR [build] ✗ api: Building api failed after 3s: npm run build exited with status 1\n" +
		"\n" +
		"    api: missing module\n" +
		"\n" +
		"INFO  [build] ✓ web: Building web in 5s\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func output(t *testing.T, unit *events.Scope, text string) {
	t.Helper()
	if _, err := unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT).Write([]byte(text + "\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
}

func debugAndInfoInAUnit(t *testing.T, present Presentation) string {
	t.Helper()
	run, out, c := groupedRun(t, present)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Debug("resolved the node toolchain at /usr/bin/node")
	web := build.Unit("web", "Building web")
	web.Debug("reusing 3 cached layers")
	web.Say("bundled 12 routes")
	if _, err := web.Output(progressv1.Level_LEVEL_DEBUG, progressv1.Stream_STREAM_STDERR).Write([]byte("npm timing ok\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	c.pass(2 * time.Second)
	web.End(nil)
	return out.String()
}

func TestAUnitsMessagesAreIndentedUnderItsHeaderAndDebugIsHiddenUnlessVerbose(t *testing.T) {
	t.Parallel()

	want := "INFO  [build] ✓ web: Building web in 2s\n" +
		"      bundled 12 routes\n"
	if got := debugAndInfoInAUnit(t, Presentation{}); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestVerboseShowsDebugLinesInTheirUnit(t *testing.T) {
	t.Parallel()

	want := "DEBUG [build] resolved the node toolchain at /usr/bin/node\n" +
		"INFO  [build] ✓ web: Building web in 2s\n" +
		"      DEBUG reusing 3 cached layers\n" +
		"      bundled 12 routes\n" +
		"\n" +
		"    npm timing ok\n"
	if got := debugAndInfoInAUnit(t, Presentation{Verbose: true}); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAUnitsProgressCountsAreDetailLinesUnderItsHeader(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	start := c.now()
	total := uint32(2)
	deploy.Forward(providerStarted(1, "web", "uploaded 2 assets", start))
	deploy.Forward(providerEvent(1, "web", &progressv1.OperationEvent{
		Message: "uploading assets",
		Body:    &progressv1.OperationEvent_Counter{Counter: &progressv1.Counter{Current: 1, Total: &total}},
	}))
	deploy.Forward(providerEnded(1, "web", progressv1.SpanStatus_SPAN_STATUS_OK, start, start.Add(time.Second)))

	want := "INFO  [deploy] ✓ web: uploaded 2 assets in 1s\n" +
		"      uploading assets (1/2)\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAUnitStillOpenWhenTheSinkClosesPrintsWhatItBufferedAsUnfinished(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	sink := newGroupedSink(&out, Presentation{}, nil)
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, context.Background(), c.now, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Forward(providerStarted(1, "api", "deploying api", c.now()))
	deploy.Forward(providerOutput(1, "api", "updating function api"))
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	want := "WARN  [deploy] api: deploying api did not finish\n" +
		"\n" +
		"    updating function api\n" +
		"\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWhatAUnitsChildScopesSayBelongsToTheUnitsBlock(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	start := c.now()
	deploy.Forward(providerStarted(1, "web", "deployed 4 resources", start))
	child := providerStarted(2, "web", "Uploading", start)
	child.GetStarted().ParentSpanId = []byte{1, 0, 0, 0, 0, 0, 0, 1}
	deploy.Forward(child)
	deploy.Forward(providerOutput(2, "web", "creating bucket assets"))
	deploy.Forward(providerEnded(2, "web", progressv1.SpanStatus_SPAN_STATUS_OK, start, start.Add(time.Second)))
	if got := out.String(); got != "" {
		t.Fatalf("printed before the unit ended: %q", got)
	}
	deploy.Forward(providerEnded(1, "web", progressv1.SpanStatus_SPAN_STATUS_OK, start, start.Add(2*time.Second)))

	want := "INFO  [deploy] ✓ web: deployed 4 resources in 2s\n" +
		"\n" +
		"    creating bucket assets\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func heartbeatRun(t *testing.T) (*events.Run, *GroupedSink, chan<- time.Time, *bytes.Buffer, *clock) {
	t.Helper()
	var out bytes.Buffer
	ticks := make(chan time.Time)
	sink := newGroupedSink(&out, Presentation{}, ticks)
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, context.Background(), c.now, sink)
	return run, sink, ticks, &out, c
}

func closed(t *testing.T, sink *GroupedSink, out *bytes.Buffer) string {
	t.Helper()
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return out.String()
}

func TestThirtySecondsOfSilencePrintsAHeartbeatNamingWhatIsStillRunning(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Unit("web", "Building web")
	build.Unit("api", "Building api")
	start := c.now()
	ticks <- start.Add(29 * time.Second)
	ticks <- start.Add(30 * time.Second)

	want := "INFO  [build] Still building web, api — 0/2 done, 30s elapsed\n" +
		"WARN  [build] web: Building web did not finish\n" +
		"WARN  [build] api: Building api did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTheNextHeartbeatComesSixtySecondsLater(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "deploying api")
	start := c.now()
	ticks <- start.Add(30 * time.Second)
	ticks <- start.Add(89 * time.Second)
	ticks <- start.Add(90 * time.Second)

	want := "INFO  [deploy] Still deploying api — 0/1 done, 30s elapsed\n" +
		"INFO  [deploy] Still deploying api — 0/1 done, 1m30s elapsed\n" +
		"WARN  [deploy] api: deploying api did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAPrintedLineRestartsTheSilence(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	build.Unit("api", "Building api")
	start := c.now()
	c.pass(30 * time.Second)
	web.End(nil)
	ticks <- start.Add(59 * time.Second)
	ticks <- start.Add(60 * time.Second)

	want := "INFO  [build] ✓ web: Building web in 30s\n" +
		"INFO  [build] Still building api — 1/2 done, 1m00s elapsed\n" +
		"WARN  [build] api: Building api did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestSilenceWithNothingRunningPrintsNoHeartbeat(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	c.pass(time.Second)
	web.End(nil)
	ticks <- c.now().Add(5 * time.Minute)

	want := "INFO  [build] ✓ web: Building web in 1s\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestNoHeartbeatPrintsWhileTheRunWaitsOnSomeone(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	run.Hold(&streamv1.WaitingEvent{Url: "https://ocel.dev/vars"})
	ticks <- c.now().Add(5 * time.Minute)

	want := "WARN  [build] web: Building web did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTheSilenceRestartsWhenTheRunResumes(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	resume := run.Hold(&streamv1.WaitingEvent{Url: "https://ocel.dev/vars"})
	start := c.now()
	c.pass(2 * time.Minute)
	resume("every variable is set")
	ticks <- start.Add(2*time.Minute + 29*time.Second)
	ticks <- start.Add(2*time.Minute + 30*time.Second)

	want := "INFO  [build] Still building web — 0/1 done, 2m30s elapsed\n" +
		"WARN  [build] web: Building web did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestInGitHubActionsASuccessfulBlockIsACollapsedGroup(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{GitHubActions: true})
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "built 12 routes")
	output(t, web, "Compiled successfully")
	c.pass(34 * time.Second)
	web.End(nil)
	build.Say("every app is built")

	want := "::group::INFO  [build] ✓ web: built 12 routes in 34s\n" +
		"\n" +
		"    Compiled successfully\n" +
		"\n" +
		"::endgroup::\n" +
		"INFO  [build] every app is built\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestInGitHubActionsAFailedBlockStaysExpandedAndALaterSuccessKeepsItsBodyFolded(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{GitHubActions: true})
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	api := build.Unit("api", "Building api")
	output(t, web, "web compiled")
	output(t, api, "api: missing module")
	c.pass(3 * time.Second)
	api.End(errors.New("npm run build exited with status 1"))
	c.pass(2 * time.Second)
	web.End(nil)

	want := "ERROR [build] ✗ api: Building api failed after 3s: npm run build exited with status 1\n" +
		"::error::[build] api: Building api failed after 3s: npm run build exited with status 1\n" +
		"\n" +
		"    api: missing module\n" +
		"\n" +
		"::group::INFO  [build] ✓ web: Building web in 5s\n" +
		"\n" +
		"    web compiled\n" +
		"\n" +
		"::endgroup::\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestInGitHubActionsAWarningIsMirroredAsAnAnnotationWithItsNewlinesAndPercentsEscaped(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	sink := newGroupedSink(&out, Presentation{GitHubActions: true}, nil)
	sink.Receive(&streamv1.RunEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_CHECK,
		Message: "the zone is 100% over quota\r\n::error::forged",
	})
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	want := "WARN  [check] the zone is 100% over quota\r\n" +
		"      ::error::forged\n" +
		"::warning::[check] the zone is 100%25 over quota%0D%0A::error::forged\n"
	if got := out.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestInGitHubActionsAGroupTitleCannotStartAWorkflowCommand(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{GitHubActions: true})
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "built 100% of routes\n::error::forged")
	output(t, web, "Compiled successfully")
	c.pass(time.Second)
	web.End(nil)

	want := "::group::INFO  [build] ✓ web: built 100%25 of routes%0A      ::error::forged in 1s\n" +
		"\n" +
		"    Compiled successfully\n" +
		"\n" +
		"::endgroup::\n"
	if got := out.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func providerChild(span, parent byte, subject, message string, at time.Time) *progressv1.OperationEvent {
	started := providerStarted(span, subject, message, at)
	started.GetStarted().ParentSpanId = []byte{parent, 0, 0, 0, 0, 0, 0, 1}
	return started
}

func forwardResource(scope *events.Scope, span, parent byte, subject string, action provider.ChangeAction, typ, name string, status progressv1.SpanStatus, at time.Time) {
	scope.Forward(providerChild(span, parent, subject, "resource operation", at))
	ended := providerEnded(span, subject, status, at, at.Add(time.Second))
	ended.GetEnded().Attributes = []*progressv1.SpanAttribute{
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, Value: "1000"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, Value: typ},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME, Value: name},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION, Value: string(action)},
	}
	scope.Forward(ended)
}

func TestADeployBlockCountsAndListsTheResourcesItChangedAndNeverOneItLeftAlone(t *testing.T) {
	t.Parallel()

	run, out, c := groupedRun(t, Presentation{})
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	start := c.now()
	deploy.Forward(providerStarted(1, "web", "Deploying web", start))
	deploy.Forward(providerChild(2, 1, "web", "Deploying", start))
	ok := progressv1.SpanStatus_SPAN_STATUS_OK
	forwardResource(deploy, 3, 2, "web", provider.ActionCreate, "aws:s3/bucket:Bucket", "assets", ok, start)
	forwardResource(deploy, 4, 2, "web", provider.ActionUpdate, "aws:iam/role:Role", "api", ok, start)
	forwardResource(deploy, 5, 2, "web", provider.ActionCreate, "aws:sqs/queue:Queue", "jobs", ok, start)
	forwardResource(deploy, 6, 2, "web", provider.ActionDelete, "aws:sqs/queue:Queue", "old", ok, start)
	forwardResource(deploy, 7, 2, "web", provider.ActionReplace, "aws:lambda/function:Function", "handler", ok, start)
	slow := providerChild(8, 2, "web", "resource operation", start)
	deploy.Forward(slow)
	deploy.Forward(providerEnded(8, "web", ok, start, start.Add(40*time.Second)))
	deploy.Forward(providerEnded(2, "web", ok, start, start.Add(41*time.Second)))
	deploy.Forward(providerEnded(1, "web", ok, start, start.Add(42*time.Second)))

	want := "INFO  [deploy] ✓ web: Deploying web in 42s (2 created, 1 updated, 1 replaced, 1 deleted)\n" +
		"      + assets (aws:s3/bucket:Bucket) created\n" +
		"      ~ api (aws:iam/role:Role) updated\n" +
		"      + jobs (aws:sqs/queue:Queue) created\n" +
		"      – old (aws:sqs/queue:Queue) deleted\n" +
		"      ± handler (aws:lambda/function:Function) replaced\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func failedDeploy(t *testing.T, present Presentation) string {
	t.Helper()
	run, out, c := groupedRun(t, present)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	start := c.now()
	deploy.Forward(providerStarted(1, "", "Environment", start))
	deploy.Forward(providerChild(2, 1, "", "Provisioning", start))
	engine := providerOutput(2, "", "+  aws:s3:Bucket logs creating (0s) error: BucketAlreadyExists")
	engine.Level = progressv1.Level_LEVEL_DEBUG
	engine.GetOutput().Stream = progressv1.Stream_STREAM_UNSPECIFIED
	deploy.Forward(engine)
	forwardResource(deploy, 3, 2, "", provider.ActionCreate, "aws:s3/bucket:Bucket", "assets", progressv1.SpanStatus_SPAN_STATUS_OK, start)
	forwardResource(deploy, 4, 2, "", provider.ActionCreate, "aws:s3/bucket:Bucket", "logs", progressv1.SpanStatus_SPAN_STATUS_ERROR, start)
	deploy.Forward(providerEvent(2, "", &progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_ERROR,
		Message: "logs (aws:s3/bucket:Bucket): creating S3 Bucket (logs): BucketAlreadyExists",
	}))
	deploy.Forward(providerEnded(2, "", progressv1.SpanStatus_SPAN_STATUS_ERROR, start, start.Add(9*time.Second)))
	deploy.Forward(providerEnded(1, "", progressv1.SpanStatus_SPAN_STATUS_ERROR, start, start.Add(10*time.Second)))
	return out.String()
}

func TestAFailedResourcesDiagnosticIsUnderItsFailedBlockWhileTheEngineOutputStaysHidden(t *testing.T) {
	t.Parallel()

	want := "ERROR [deploy] ✗ Environment failed after 10s (1 created, 1 failed)\n" +
		"      + assets (aws:s3/bucket:Bucket) created\n" +
		"      ✗ logs (aws:s3/bucket:Bucket) failed to create\n" +
		"      ERROR logs (aws:s3/bucket:Bucket): creating S3 Bucket (logs): BucketAlreadyExists\n"
	if got := failedDeploy(t, Presentation{}); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestVerboseShowsTheEngineOutputInTheFailedBlockToo(t *testing.T) {
	t.Parallel()

	if got := failedDeploy(t, Presentation{Verbose: true}); !strings.Contains(got, "\n    +  aws:s3:Bucket logs creating (0s) error: BucketAlreadyExists\n") {
		t.Fatalf("verbose output has no engine line:\n%s", got)
	}
}

func productionRun(t *testing.T) (*events.Run, *bytes.Buffer, *clock) {
	t.Helper()
	run, out, c := groupedRun(t, Presentation{})
	run.Phase(progressv1.Phase_PHASE_CHECK).Identity(&streamv1.IdentityEvent{Project: "acme", Tier: environmentv1.Tier_TIER_PRODUCTION})
	return run, out, c
}

func forwardOutcome(run *events.Run, outcome *progressv1.ResultEvent) {
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: outcome}})
}

func ended(run *events.Run, err error) {
	run.End(&err)
}

func TestASuccessfulDeployNamesWhatProductionServesNowAndWhereEachAppIs(t *testing.T) {
	t.Parallel()

	run, out, c := productionRun(t)
	forwardOutcome(run, &progressv1.ResultEvent{Success: true, PromotionId: "p-7f3a", Apps: []*progressv1.AppResult{
		{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Urls: []string{"https://acme.example.com"}},
		{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Urls: []string{"https://api.acme.example.com"}},
	}})
	run.Deployed("Deployed acme to production", nil, nil)
	c.pass(3*time.Minute + 29*time.Second)
	ended(run, nil)

	want := "✓ Deployed acme to production in 3m29s\n" +
		"  production now serves promotion p-7f3a\n" +
		"  web  https://acme.example.com\n" +
		"  api  https://api.acme.example.com\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAFailedMultiAppDeploySaysWhatWasNotPromotedAndThatProductionStillServesWhatItDidAndNamesTheFailedApp(t *testing.T) {
	t.Parallel()

	run, out, c := productionRun(t)
	run.Phase(progressv1.Phase_PHASE_BUILD).Say("built 2 apps")
	forwardOutcome(run, &progressv1.ResultEvent{Error: "api: the stack update failed", Apps: []*progressv1.AppResult{
		{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Urls: []string{"https://acme.example.com"}},
		{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED, Error: "the stack update failed"},
	}})
	c.pass(4*time.Minute + 2*time.Second)
	ended(run, errors.New("api: the stack update failed"))

	want := "INFO  [build] built 2 apps\n" +
		"\n" +
		"✗ Failed in 4m02s — api: the stack update failed\n" +
		"  web deployed but was not promoted: promotion needs every app, and api failed\n" +
		"  production still serves what it served before this run\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAUnitStillOpenWhenTheResultArrivesPrintsAsUnfinishedAboveTheSummary(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	sink := newGroupedSink(&out, Presentation{}, nil)
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, context.Background(), c.now, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Forward(providerStarted(1, "api", "deploying api", c.now()))
	deploy.Forward(providerOutput(1, "api", "updating function api"))
	c.pass(12 * time.Second)
	ended(run, errors.New("the provider exited"))
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	want := "WARN  [deploy] api: deploying api did not finish\n" +
		"\n" +
		"    updating function api\n" +
		"\n" +
		"✗ Failed in 12s — the provider exited\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTheSummaryEndsWithTheNotesOnItsUrlsAndWhereTheRunsLogIs(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	sink := newGroupedSink(&out, Presentation{}, nil)
	sink.Receive(resultEvent(&streamv1.RunResultEvent{
		Success:    true,
		Headline:   "Preview pr-12 is up",
		DurationMs: 41_000,
		Apps:       []*progressv1.AppResult{{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Urls: []string{"https://pr-12.acme.example.com"}}},
		UrlNotes:   []string{"web: the custom domain shop.acme.com is not attached to previews"},
		FlipBound:  &progressv1.FlipBound{TypicalMs: 5000, Published: true},
		LogPath:    "/var/ocel-runs/0af3.ndjson",
	}))

	want := "✓ Preview pr-12 is up in 41s\n" +
		"  web  https://pr-12.acme.example.com\n" +
		"  web: the custom domain shop.acme.com is not attached to previews\n" +
		"  propagates within ~5 s\n" +
		"  Log: /var/ocel-runs/0af3.ndjson\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestACancelledRunsSummarySaysSoAndWhatToRerunAfterAVerbatimBlockWithOneBlankLine(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, ctx, c.now, newGroupedSink(&out, Presentation{}, nil))
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	web := deploy.Unit("web", "deploying web")
	output(t, web, "creating function web")
	web.End(nil)
	c.pass(12 * time.Second)
	cancel()
	ended(run, context.Canceled)

	want := "INFO  [deploy] ✓ web: deploying web in 0s\n" +
		"\n" +
		"    creating function web\n" +
		"\n" +
		"✗ Cancelled in 12s — Resources may be partially created.\n" +
		"  Re-run `ocel deploy` to reconcile.\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestARunRefusedForMissingVariablesListsThemAndWhereToFillThemIn(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	sink := newGroupedSink(&out, Presentation{}, nil)
	sink.Receive(resultEvent(&streamv1.RunResultEvent{DurationMs: 3000, Missing: missingStripeKey()}))

	want := "✗ 1 variable is not ready — nothing has been built.\n" +
		"\n" +
		"  ✗ STRIPE_API_KEY  root  no value\n" +
		"\n" +
		"  Fill them in: ocel env ui\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func failedWith(t *testing.T, apps ...*progressv1.AppResult) string {
	t.Helper()
	run, out, _ := productionRun(t)
	forwardOutcome(run, &progressv1.ResultEvent{Error: "deploy failed", Apps: apps})
	ended(run, errors.New("deploy failed"))
	return out.String()
}

func TestAFailedSingleAppDeploySaysNothingWasPromoted(t *testing.T) {
	t.Parallel()

	got := failedWith(t, &progressv1.AppResult{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED})

	want := "✗ Failed in 0s — deploy failed\n" +
		"  nothing was promoted: web failed\n" +
		"  production still serves what it served before this run\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestADeployThatFailedBeforeReachingSomeAppsNamesThemAsNotRun(t *testing.T) {
	t.Parallel()

	got := failedWith(t,
		&progressv1.AppResult{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED},
		&progressv1.AppResult{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_NOT_RUN},
		&progressv1.AppResult{App: "cron", Outcome: progressv1.AppOutcome_APP_OUTCOME_NOT_RUN},
	)

	want := "✗ Failed in 0s — deploy failed\n" +
		"  nothing was promoted: promotion needs every app, and web failed and api and cron did not run\n" +
		"  production still serves what it served before this run\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAFailureAfterEveryAppDeployedClaimsNothingAboutWhatProductionServes(t *testing.T) {
	t.Parallel()

	got := failedWith(t,
		&progressv1.AppResult{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
		&progressv1.AppResult{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
	)

	if want := "✗ Failed in 0s — deploy failed\n"; got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAppsThatDeployedBesideAFailedOneAreNamedAsNotPromoted(t *testing.T) {
	t.Parallel()

	got := failedWith(t,
		&progressv1.AppResult{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
		&progressv1.AppResult{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
		&progressv1.AppResult{App: "cron", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED},
	)

	want := "✗ Failed in 0s — deploy failed\n" +
		"  web and api deployed but were not promoted: promotion needs every app, and cron failed\n" +
		"  production still serves what it served before this run\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
