package runui

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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
