package runui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var (
	environmentUnit = naming.UnitID(naming.UnitEnvironment)
	testStageID     = naming.PhaseID(naming.UnitEnvironment, naming.PhaseProvisioning)
)

func humanTranscript(t *testing.T, present Presentation) (*HumanSink, *safeBuffer) {
	t.Helper()
	present.Format = FormatHuman
	if present.Width == 0 {
		present.Width = defaultWidth
	}
	return drivenStream(t, present)
}

func startProvisioning(s *HumanSink) {
	startAll(s,
		scope{id: environmentUnit, title: "Environment"},
		scope{id: testStageID, parent: environmentUnit, phase: progressv1.Phase_PHASE_PROVISION},
	)
}

func closeProvisioning() *streamv1.RunEvent {
	return endedEvent(testStageID, false, time.Second)
}

func logLine(line string) *streamv1.RunEvent {
	return outputEvent(testStageID, line)
}

func progress(message string) *streamv1.RunEvent {
	return progressEvent(testStageID, message, 0, nil)
}

func resultEvent(result *streamv1.RunResultEvent) *streamv1.RunEvent {
	return &streamv1.RunEvent{Level: progressv1.Level_LEVEL_INFO, Body: &streamv1.RunEvent_Result{Result: result}}
}

func missingStripeKey() *streamv1.MissingVariables {
	return &streamv1.MissingVariables{
		Cells:  []*streamv1.MissingVariable{{Key: "STRIPE_API_KEY", Reason: "no value"}},
		Remedy: "ocel env ui",
	}
}

func onABus(t *testing.T, ctx context.Context, now func() time.Time, sink events.Sink) (*events.Bus, *events.Run) {
	t.Helper()
	bus := events.NewBus(now)
	bus.Attach(sink)
	_, run, err := bus.Begin(ctx, "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return bus, run
}

func TestAnInterruptTakesTheLiveFrameBackAndFlushesWhatWasInFlight(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	sink := newHumanSink(&out, Presentation{Format: FormatHuman, TTY: true, Verbose: true, Width: defaultWidth, Height: defaultHeight})
	ctx, cancel := context.WithCancel(context.Background())
	bus, run := onABus(t, ctx, time.Now, sink)

	unit := run.Phase(progressv1.Phase_PHASE_PROVISION).Unit("aws", "Environment")
	unit.Say("provisioning the account")
	if _, err := unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT).Write([]byte("a line the run never finished")); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	cancel()
	bus.Interrupt()

	if got := sink.r.liveLines; got != 0 {
		t.Errorf("liveLines = %d, want the live frame taken back so no frame is committed to the scrollback", got)
	}
	got := out.String()
	if !strings.Contains(got, "a line the run never finished") {
		t.Errorf("stdout = %q, want the in-flight line flushed by the interrupt", got)
	}
	if !strings.Contains(got, warnMark+" Cancelled") {
		t.Errorf("stdout = %q, want the run to say where it stopped", got)
	}
}

func TestProgressLandsInItsFlushedBlockAndAnUnclaimedDebugLineNeverReachesTheTerminal(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	startProvisioning(s)
	s.Receive(progress("Uploading function artifacts"))
	s.Receive(&streamv1.RunEvent{Level: progressv1.Level_LEVEL_DEBUG, Message: "pulumi engine line", Body: &streamv1.RunEvent_Output{Output: &progressv1.Output{}}})
	s.Receive(closeProvisioning())
	s.Receive(resultEvent(&streamv1.RunResultEvent{
		Success:  true,
		Headline: "Deployed",
		Apps:     []*progressv1.AppResult{{App: "web", Urls: []string{"https://app.example.workers.dev"}}},
	}))

	got := out.String()
	for _, want := range []string{"Uploading function artifacts", "Deployed in", "https://app.example.workers.dev"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "pulumi engine line") {
		t.Errorf("stdout = %q, want a debug line no stage claims kept out of the app-major view", got)
	}
}

func TestAFailureRendersTheErrorAndPointsAtTheRunsLog(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(resultEvent(&streamv1.RunResultEvent{
		Detail:  "creating rds: InsufficientCapacity",
		LogPath: "/srv/shop/runs/0af7651916cd43dd8448eb211c80319c.ndjson",
	}))

	got := out.String()
	if !strings.Contains(got, "creating rds: InsufficientCapacity") {
		t.Errorf("stdout = %q, want the error message", got)
	}
	if !strings.Contains(got, ".ndjson") {
		t.Errorf("stdout = %q, want a pointer to the run's log", got)
	}
}

func TestAFailureListsWhatASlowDeleteLeftInPlaceOneItemToALine(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(resultEvent(&streamv1.RunResultEvent{Detail: (&edge.OutstandingError{
		Because: "API Gateway paces deletions",
		Waited:  14*time.Minute + 30*time.Second,
		Items: []edge.Outstanding{
			{Kind: "REST API", Name: "api1"},
			{Kind: "REST API", Name: "api2"},
		},
	}).Error()}))

	got := out.String()
	for _, want := range []string{"re-run the same command", "• REST API api1", "• REST API api2"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "api1  • REST API api2") {
		t.Errorf("stdout = %q, want each outstanding item on its own line", got)
	}
}

func TestAFailureWithNoActiveStepStillPrintsAFailureLine(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(resultEvent(&streamv1.RunResultEvent{Detail: "boom"}))

	if !strings.Contains(out.String(), "Failed") {
		t.Errorf("stdout = %q, want a bare Failed line", out.String())
	}
}

func TestACancelledRunIsMarkedAsAnInterruptionNotAFailure(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(progress("Provisioning resources"))
	s.Receive(resultEvent(&streamv1.RunResultEvent{
		Interrupted: true,
		Headline:    "Cancelled",
		Detail:      "Resources may be partially created.\nRe-run `ocel deploy` to reconcile.",
	}))

	got := out.String()
	for _, want := range []string{warnMark + " Cancelled", "partially created", "ocel deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
}

func TestAWaitPrintsWhereToGoAndHowToAbort(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{
		Missing: missingStripeKey(),
		Url:     "http://127.0.0.1:5555/#t=abc",
	}}})

	got := out.String()
	for _, want := range []string{"STRIPE_API_KEY", "http://127.0.0.1:5555/#t=abc", "Ctrl-C"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
}

func TestARefusalIsTheFailureNotADetailUnderOne(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})
	_, run := onABus(t, context.Background(), time.Now, s)

	err := errors.Join(&envgate.Refusal{Problems: []*resourcesv1.VariableProblem{
		{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
	}}, errors.New("the variables UI closed before the matrix was complete."))
	run.End(&err)

	got := out.String()
	if want := "✗ 1 variable is not ready — nothing has been built.\n\n  ✗ STRIPE_API_KEY  root  no value\n\n  Fill them in: ocel env set STRIPE_API_KEY=<VALUE>\n\n  the variables UI closed"; !strings.Contains(got, want) {
		t.Errorf("stdout = %q, want it to contain %q", got, want)
	}
	if strings.Contains(got, "✗ Failed") {
		t.Errorf("stdout = %q, want the refusal kept as the failure headline", got)
	}
}

func TestAStartedScopeEntersTheRenderersTreeUnderItsTitleOrItsPhaseLabel(t *testing.T) {
	t.Parallel()
	s, _ := humanTranscript(t, Presentation{})

	build, stage := appStage(1), appStage(2)
	startAll(s, scope{id: build, title: "Building"}, scope{id: stage, phase: progressv1.Phase_PHASE_PROVISION})

	if title := s.r.plan.nodes[stageKey(build)].title; title != "Building" {
		t.Errorf("stage title = %q, want %q", title, "Building")
	}
	if title := s.r.plan.nodes[stageKey(stage)].title; title != "Provisioning" {
		t.Errorf("stage title = %q, want the phase label the declaration names", title)
	}
}

func TestAChildStageArrivingBeforeItsParentStillAttaches(t *testing.T) {
	t.Parallel()
	plan := newStagePlan()
	parent := []byte{9, 0, 0, 0, 0, 0, 0, 0}
	child := []byte{10, 0, 0, 0, 0, 0, 0, 0}

	declareAll(plan, scope{id: child, parent: parent, title: "app-a"})
	if _, ok := plan.nodes[stageKey(child)]; !ok {
		t.Fatal("orphan child was not recorded at all")
	}
	if plan.nodes[stageKey(child)].linked {
		t.Error("orphan child linked before its parent arrived")
	}

	declareAll(plan, scope{id: parent, title: "apps"})

	parentNode := plan.nodes[stageKey(parent)]
	if len(parentNode.children) != 1 || parentNode.children[0] != stageKey(child) {
		t.Errorf("parent.children = %v, want the orphan attached", parentNode.children)
	}
	if !plan.nodes[stageKey(child)].linked {
		t.Error("child was not marked linked once its parent arrived")
	}
}

func TestProviderProcessOutputShowsOnlyWhenVerboseAndNeverEntersABlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		origin Origin
		shown  bool
	}{
		{"a terminal", Origin{LogFormat: "human", TTY: true}, false},
		{"a terminal with --verbose", Origin{LogFormat: "human", TTY: true, Verbose: true}, true},
		{"a pipe", Origin{LogFormat: "human"}, false},
		{"a pipe with --verbose", Origin{LogFormat: "human", Verbose: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, out := drivenStream(t, Resolve(tc.origin))

			const marker = "raw subprocess output"
			startProvisioning(s)
			s.Receive(progress("a line the phase owns"))
			s.Receive(&streamv1.RunEvent{Level: progressv1.Level_LEVEL_DEBUG, Subject: "aws", Message: marker, Body: &streamv1.RunEvent_Output{
				Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDOUT},
			}})
			s.Receive(closeProvisioning())

			got := scrollback(out.String())
			if shown := strings.Contains(got, marker+"\n"); shown != tc.shown {
				t.Errorf("stdout = %q, shows the provider process output = %v, want %v: it is debug output", got, shown, tc.shown)
			}
			if strings.Contains(got, blockIndent+marker) {
				t.Errorf("stdout = %q, want global text kept out of the phase block, which belongs to the unit", got)
			}
			if at, block := strings.Index(got, marker), strings.Index(got, blockIndent+"a line the phase owns"); tc.shown && at > block {
				t.Errorf("stdout = %q, want the line committed as it landed, before the block it interrupted flushed", got)
			}
		})
	}
}

func TestAUnitRepaintingOneLineCommitsOnlyTheDraftItLeft(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{Verbose: true})
	_, run := onABus(t, context.Background(), time.Now, s)

	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	unit := build.Unit("shop", "Building project")
	w := unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	for i := 1; i <= 500; i++ {
		if _, err := fmt.Fprintf(w, "\rProgress: resolved %d", i); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	unit.End(nil)
	build.End(nil)

	got := out.String()
	if strings.Contains(got, "Progress: resolved 1\n") || strings.Count(got, "Progress: resolved") != 1 {
		t.Errorf("stdout = %q, want the repainted line committed once, as the draft the build left behind", got)
	}
	if !strings.Contains(got, blockIndent+"Progress: resolved 500\n") {
		t.Errorf("stdout = %q, want the last draft inside the build block", got)
	}
}

func TestAMessageWithNoScopeAlwaysReachesTheTerminalRegardlessOfVerbosity(t *testing.T) {
	t.Parallel()
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%t", verbose), func(t *testing.T) {
			t.Parallel()
			s, out := humanTranscript(t, Presentation{Verbose: verbose})

			s.Receive(diagnosticEvent("no functions to deploy; deploying infrastructure only"))

			if !strings.Contains(out.String(), "no functions to deploy; deploying infrastructure only") {
				t.Errorf("stdout = %q, want the message always visible", out.String())
			}
		})
	}
}

func TestAnEndedScopeWithoutAUsableEndRunsUntilItReachedTheBus(t *testing.T) {
	t.Parallel()

	stage := []byte{7, 0, 0, 0, 0, 0, 0, 0}
	now := time.Now().UTC()
	start := now.Add(-2 * time.Minute)

	for _, tc := range []struct {
		name string
		end  int64
	}{
		{"missing end", 0},
		{"end before start", start.Add(-time.Minute).UnixNano()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := humanTranscript(t, Presentation{})
			s.r.useClock(func() time.Time { return now })
			_, run := onABus(t, context.Background(), func() time.Time { return now }, s)

			deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
			deploy.Forward(&progressv1.OperationEvent{SpanId: stage, Message: "Provisioning", Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{}}})
			deploy.Forward(&progressv1.OperationEvent{TimeUnixNano: tc.end, SpanId: stage, Body: &progressv1.OperationEvent_Ended{
				Ended: &progressv1.Ended{Status: progressv1.SpanStatus_SPAN_STATUS_OK, StartTimeUnixNano: start.UnixNano()},
			}})

			if got := s.r.plan.nodes[stageKey(stage)].doneDur; got != 2*time.Minute {
				t.Errorf("committed duration = %v, want the 2m the stage ran until its end reached the bus", got)
			}
		})
	}
}

func TestBar(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		current, total uint32
		wantFilled     int
	}{
		{"is empty at zero", 0, 5, 0},
		{"is full at the total", 5, 5, barWidth},
		{"stays full past the total", 10, 5, barWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bar(tc.current, tc.total)
			if filled := strings.Count(got, "█"); filled != tc.wantFilled {
				t.Errorf("bar(%d,%d) filled = %d, want %d", tc.current, tc.total, filled, tc.wantFilled)
			}
		})
	}
}

func TestEveryEnvironmentBlockNamesThePhaseThatFilledIt(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	buildStageID := naming.PhaseID(naming.UnitEnvironment, naming.PhaseBuilding)
	uploadStageID := naming.PhaseID(naming.UnitEnvironment, naming.PhaseUploading)
	startAll(s,
		scope{id: environmentUnit, title: "Environment"},
		scope{id: buildStageID, parent: environmentUnit, title: "Building", phase: progressv1.Phase_PHASE_BUILD},
		scope{id: testStageID, parent: environmentUnit, title: "Provisioning", phase: progressv1.Phase_PHASE_PROVISION},
	)
	s.Receive(progressEvent(buildStageID, "Building project", 0, nil))
	s.Receive(endedEvent(buildStageID, false, time.Second))
	startAll(s, scope{id: uploadStageID, parent: environmentUnit, title: "Uploading", phase: progressv1.Phase_PHASE_DEPLOY})
	s.Receive(progress("provisioning the account"))
	s.Receive(endedEvent(testStageID, false, time.Second))
	s.Receive(progressEvent(uploadStageID, "uploading the bundle", 0, nil))
	s.Receive(endedEvent(uploadStageID, false, time.Second))

	got := out.String()
	for _, want := range []string{"Environment › Building", "Environment › Provisioning", "Environment › Uploading"} {
		if !strings.Contains(got, okMark+" "+want+"  ") {
			t.Errorf("transcript = %q, want the block headed %q — the unit reads the same way in every block it closes", got, want)
		}
	}
	if strings.Contains(got, okMark+" Environment  ") {
		t.Errorf("transcript = %q, want no block headed by the unit alone: the Environment unit runs more than one phase", got)
	}
}

func TestAFailedPhaseShowsItsRawOutputWhateverTheVerbosity(t *testing.T) {
	t.Parallel()

	t.Run("the phase's own span records the failure", func(t *testing.T) {
		t.Parallel()
		s, out := humanTranscript(t, Presentation{})

		startProvisioning(s)
		s.Receive(logLine("error: creating bucket assets: AccessDenied"))
		s.Receive(endedEvent(testStageID, true, time.Second))

		if got := out.String(); !strings.Contains(got, blockIndent+"error: creating bucket assets: AccessDenied\n") {
			t.Errorf("stdout = %q, want the failed phase's raw output shown without being asked twice", got)
		}
	})

	t.Run("the run ends before the phase's span arrives", func(t *testing.T) {
		t.Parallel()
		s, out := humanTranscript(t, Presentation{})

		startProvisioning(s)
		s.Receive(logLine("error: creating bucket assets: AccessDenied"))
		s.Receive(resultEvent(&streamv1.RunResultEvent{Detail: "provision production: AccessDenied"}))

		if got := out.String(); !strings.Contains(got, blockIndent+"error: creating bucket assets: AccessDenied\n") {
			t.Errorf("stdout = %q, want the block stranded by the failure to show what it contained", got)
		}
	})
}

func TestAnOrphanLogWaitsForItsStageAndFoldsIntoThatStagesBlock(t *testing.T) {
	t.Parallel()

	t.Run("addressed to a phase started later", func(t *testing.T) {
		t.Parallel()
		s, out := humanTranscript(t, Presentation{Verbose: true})

		s.Receive(logLine("the vertex spoke first"))
		if got := out.String(); strings.Contains(got, "the vertex spoke first") {
			t.Fatalf("stdout = %q, want an orphan buffered until its stage starts, never committed out of band", got)
		}

		startProvisioning(s)
		s.Receive(logLine("and again once it was started"))
		s.Receive(closeProvisioning())

		got := out.String()
		for _, want := range []string{blockIndent + "the vertex spoke first", blockIndent + "and again once it was started"} {
			if !strings.Contains(got, want+"\n") {
				t.Errorf("stdout = %q, want %q inside the flushed block", got, want)
			}
		}
		if at, closed := strings.Index(got, "the vertex spoke first"), strings.Index(got, okMark+" Environment  "); closed < 0 || at < closed {
			t.Errorf("stdout = %q, want the adopted orphan flushed under its block header rather than before it", got)
		}
	})

	t.Run("addressed to a detail scope started later under a phase", func(t *testing.T) {
		t.Parallel()
		s, out := humanTranscript(t, Presentation{Verbose: true})

		vertex := []byte{9, 9, 9, 9, 9, 9, 9, 9}
		s.Receive(outputEvent(vertex, "[build 6/9] RUN pnpm build"))
		startProvisioning(s)
		startAll(s, scope{id: vertex, parent: testStageID, title: "RUN pnpm build"})
		s.Receive(closeProvisioning())

		if got := out.String(); !strings.Contains(got, blockIndent+"[build 6/9] RUN pnpm build\n") {
			t.Errorf("stdout = %q, want the orphan folded into the block of the phase its stage turned out to sit under", got)
		}
	})
}

func TestAnOrphanWhoseStageNeverStartsNeverCommits(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(outputEvent([]byte{1, 2, 3, 4, 5, 6, 7, 8}, "a stage nothing ever started"))
	startProvisioning(s)
	s.Receive(closeProvisioning())
	s.Receive(resultEvent(&streamv1.RunResultEvent{Success: true, Headline: "Deployed"}))

	if got := out.String(); strings.Contains(got, "a stage nothing ever started") {
		t.Errorf("stdout = %q, want the orphan kept out of the human projection — nothing commits out of band", got)
	}
}

func TestABlockCommitsItsLinesVerbatimRightHandWhitespaceIncluded(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{Verbose: true})

	const padded = "Route (app)                     Size     First Load JS   "
	startProvisioning(s)
	s.Receive(logLine(padded))
	s.Receive(logLine(""))
	s.Receive(closeProvisioning())

	got := out.String()
	if !strings.Contains(got, blockIndent+padded+"\n") {
		t.Errorf("stdout = %q, want the line as the stream sent it — only carriage returns collapse, and output is complete on success", got)
	}
	if strings.Contains(got, "\n"+blockIndent+"\n") {
		t.Errorf("stdout = %q, want an empty line dropped rather than committed as a bare indent", got)
	}
}

func TestAProvidersMessageOnlyWarningIsAWarnLine(t *testing.T) {
	t.Parallel()
	s, out := humanTranscript(t, Presentation{})

	s.Receive(&streamv1.RunEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_CHECK,
		Subject: "relay",
		Message: "this deploy could not confirm the account may run code at the relay edge",
	})

	if got, want := out.String(), warnMark+" this deploy could not confirm the account may run code at the relay edge\n"; !strings.HasPrefix(got, want) {
		t.Errorf("terminal = %q, want it to open with %q", got, want)
	}
}
