package run_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEverySpanARunOpensEndsExactlyOnceBeforeItsResultWhenTheRunFails(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	check.Unit("fake", progress.Checking.Title("credentials")).End(nil)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Unit("web", progress.Deploying.Title("web"))
	check.End(nil)

	err := errors.New("the upload was refused")
	run.End(&err)

	opened, ended := map[string]bool{}, map[string]int{}
	var order []string
	got := sink.received()
	for i, ev := range got {
		id := fmt.Sprintf("%x", ev.GetSpanId())
		switch {
		case ev.GetStarted() != nil:
			opened[id] = true
		case ev.GetEnded() != nil:
			ended[id]++
			order = append(order, ev.GetSubject())
			if i == len(got)-1 {
				t.Fatal("a span ended after the run's result")
			}
		}
	}
	for id := range opened {
		if ended[id] != 1 {
			t.Fatalf("span %s ended %d times, want once", id, ended[id])
		}
	}
	if strings.Join(order, ",") != "fake,,web," {
		t.Fatalf("ended subjects in order = %q, want fake, check, then web before its deploy phase", order)
	}
	web := got[len(got)-3]
	if web.GetSubject() != "web" || web.GetLevel() != progressv1.Level_LEVEL_ERROR || web.GetMessage() != "the upload was refused" {
		t.Fatalf("the open unit ended with level %s message %q, want the run's error", web.GetLevel(), web.GetMessage())
	}
}

func TestAUnitOpenedOnAnEndedSpanStillEndsOnceWhenTheRunEnds(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	check.End(nil)
	check.Unit("fake", progress.Checking.Title("credentials")).Say("still here")

	var err error
	run.End(&err)

	ended := 0
	got := sink.received()
	for _, ev := range got[:len(got)-1] {
		if ev.GetEnded() != nil && ev.GetSubject() == "fake" {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("the unit opened after its phase ended ended %d times before the result, want once", ended)
	}
}

func TestAFailedRunEndsWithAnErrorResultAndExitsWithOne(t *testing.T) {
	sink := &recording{}
	run, clock := begin(t, sink)
	clock.advance(1500 * time.Millisecond)

	err := errors.New("the upload was refused")
	run.End(&err)

	ev := sink.received()[len(sink.received())-1]
	result := ev.GetSummary()
	if result.GetSuccess() || result.GetDetail() != "the upload was refused" || ev.GetLevel() != progressv1.Level_LEVEL_ERROR {
		t.Fatalf("result = success %v detail %q level %s, want a failure carrying the error at ERROR",
			result.GetSuccess(), result.GetDetail(), ev.GetLevel())
	}
	if result.GetDurationMs() != 1500 {
		t.Fatalf("duration = %dms, want 1500ms", result.GetDurationMs())
	}
	var exit *exitcode.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("err = %v, want an exit with code 1", err)
	}
}

func TestARunThatFailsWithAnExitCodeExitsWithThatCode(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	var err error = &exitcode.ExitError{Code: 7}
	run.End(&err)

	if result := sink.received()[len(sink.received())-1].GetSummary(); result.GetSuccess() || result.GetDetail() != "exit status 7" {
		t.Fatalf("result = success %v detail %q, want a failure naming the exit status", result.GetSuccess(), result.GetDetail())
	}
	var exit *exitcode.ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("err = %v, want an exit with code 7", err)
	}
}

func TestAFailedRunForMissingVariablesCarriesThemOnItsResult(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	var err error = &variables.MissingError{Problems: []*resourcesv1.VariableProblem{{
		Key:  "DATABASE_URL",
		Kind: resourcesv1.VariableProblem_KIND_MISSING,
	}}}
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetSummary()
	if cells := result.GetMissing().GetCells(); len(cells) != 1 || cells[0].GetKey() != "DATABASE_URL" {
		t.Fatalf("missing = %v, want DATABASE_URL", cells)
	}
	if result.GetDetail() != "" {
		t.Fatalf("detail = %q, want nothing beyond the missing variables", result.GetDetail())
	}
}

func TestAnInterruptedRunEndsCancelledAtWarnAndExitsAsInterrupted(t *testing.T) {
	sink := &recording{}
	ctx, cancel := context.WithCancel(context.Background())
	run, _ := beginIn(t, ctx, sink)
	run.Phase(progressv1.Phase_PHASE_BUILD)

	cancel()
	err := context.Canceled
	run.End(&err)

	ev := sink.received()[len(sink.received())-1]
	if !ev.GetSummary().GetInterrupted() || ev.GetSummary().GetHeadline() != "Deploy cancelled" || ev.GetLevel() != progressv1.Level_LEVEL_WARN {
		t.Fatalf("result = interrupted %v headline %q level %s, want Deploy cancelled at WARN",
			ev.GetSummary().GetInterrupted(), ev.GetSummary().GetHeadline(), ev.GetLevel())
	}
	var exit *exitcode.ExitError
	if !errors.As(err, &exit) || exit.Code != exitcode.Interrupt {
		t.Fatalf("err = %v, want the interrupt exit", err)
	}
}

func TestAnInterruptedRunWarnsOfPartlyCreatedResourcesOnlyOnceAPhaseThatChangesThemStarted(t *testing.T) {
	const partly = "Resources may be partially created.\nRe-run `ocel deploy` to reconcile."
	for _, tc := range []struct {
		name   string
		work   func(run *run.Run)
		detail string
	}{
		{name: "checking and building", work: func(run *run.Run) {
			run.Phase(progressv1.Phase_PHASE_CHECK).End(nil)
			run.Phase(progressv1.Phase_PHASE_BUILD).Hold(&streamv1.WaitingEvent{})
		}},
		{name: "planning", work: func(run *run.Run) {
			run.Phase(progressv1.Phase_PHASE_PLAN).Unit("shop", progress.Planning.Title("changes"))
		}},
		{name: "a phase the command opened to promote", detail: partly, work: func(run *run.Run) {
			run.Phase(progressv1.Phase_PHASE_PROMOTE).Unit("shop", progress.Title{Started: "Promoting d-1", Ended: "Promoting d-1"})
		}},
		{name: "a phase the provider reported destroying in", detail: partly, work: func(run *run.Run) {
			run.Phase(progressv1.Phase_PHASE_CHECK).Forward(&progressv1.OperationEvent{
				Phase:  progressv1.Phase_PHASE_DESTROY,
				SpanId: []byte("unit-env"),
				Body:   &progressv1.OperationEvent_Started{Started: &progressv1.Started{}},
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recording{}
			ctx, cancel := context.WithCancel(context.Background())
			run, _ := beginIn(t, ctx, sink)
			tc.work(run)

			cancel()
			err := context.Canceled
			run.End(&err)

			if got := sink.received()[len(sink.received())-1].GetSummary().GetDetail(); got != tc.detail {
				t.Fatalf("detail = %q, want %q", got, tc.detail)
			}
		})
	}
}

func TestASuccessfulRunEndsWithASuccessResultAndNoError(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Say("Deployed")

	var err error
	run.End(&err)

	got := sink.received()
	if ev := got[len(got)-1]; !ev.GetSummary().GetSuccess() || ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		t.Fatalf("result = success %v level %s, want success at INFO", ev.GetSummary().GetSuccess(), ev.GetLevel())
	}
	if ended := got[len(got)-2]; ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Fatalf("the deploy phase ended %s, want OK", ended.GetEnded().GetStatus())
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !bytes.Equal(got[len(got)-2].GetSpanId(), got[0].GetSpanId()) {
		t.Fatal("the run ended some other span than its deploy phase")
	}
}

func TestASucceededRunsSummaryCarriesItsHeadlineAndTheURLNotesAndPropagationItsProviderReported(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Forward(&progressv1.OperationEvent{Phase: progressv1.Phase_PHASE_DEPLOY, Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
		Success:     true,
		UrlNotes:    []string{"web: https://web.example.com"},
		Propagation: &progressv1.Propagation{TypicalMs: 3000, Published: true},
	}}})
	deploy.End(nil)
	run.Succeed("Deployed")

	var err error
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetSummary()
	if !result.GetSuccess() || result.GetHeadline() != "Deployed" ||
		strings.Join(result.GetUrlNotes(), ",") != "web: https://web.example.com" || result.GetPropagation().GetTypicalMs() != 3000 {
		t.Fatalf("result = %s, want a success headed Deployed with its url notes and propagation", protojson.Format(result))
	}
}

func TestARunThatFailsAfterReportingAHeadlineEndsWithTheFailureInstead(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	run.Succeed("Nothing to deploy")

	err := errors.New("the service map could not be written")
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetSummary()
	if result.GetSuccess() || result.GetHeadline() != "Deploy failed" || result.GetDetail() != "the service map could not be written" {
		t.Fatalf("result = %s, want the failure headed Deploy failed, not the success headline", protojson.Format(result))
	}
}

func TestAForwardedProviderEventReachesTheSinksAsARunEventWithItsEnvelope(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	at := time.Date(2026, 9, 27, 12, 0, 5, 0, time.UTC)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{
		Time:    timestamppb.New(at),
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_PROVISION,
		Subject: "web",
		Message: "Provisioning web",
		SpanId:  []byte("unit-web"),
		Body:    &progressv1.OperationEvent_Started{Started: &progressv1.Started{ParentSpanId: []byte("phase-01")}},
	})

	ev := sink.received()[1]
	if !ev.GetTime().AsTime().Equal(at) || ev.GetLevel() != progressv1.Level_LEVEL_WARN || ev.GetPhase() != progressv1.Phase_PHASE_PROVISION ||
		ev.GetSubject() != "web" || ev.GetMessage() != "Provisioning web" || string(ev.GetSpanId()) != "unit-web" ||
		string(ev.GetStarted().GetParentSpanId()) != "phase-01" {
		t.Fatalf("forwarded = time %s level %s phase %s subject %q message %q span %q parent %q, want the provider's envelope and body",
			ev.GetTime().AsTime(), ev.GetLevel(), ev.GetPhase(), ev.GetSubject(), ev.GetMessage(), ev.GetSpanId(), ev.GetStarted().GetParentSpanId())
	}
}

func TestAnEventWithNoTimeOrLevelOfItsOwnLandsStampedWithTheMomentItArrivedAtInfo(t *testing.T) {
	sink := &recording{}
	run, c := begin(t, sink)
	c.advance(3 * time.Second)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{Message: "no functions to deploy; deploying infrastructure only"})

	ev := sink.received()[1]
	if !ev.GetTime().AsTime().Equal(c.read()) || ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		t.Fatalf("the event landed at %s level %s, want %s at INFO", ev.GetTime().AsTime(), ev.GetLevel(), c.read())
	}
}

func TestAProviderLineRewrittenWithCarriageReturnsReachesEverySinkAsTheLastThingItSaid(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)

	for _, message := range []string{"uploading 10%\ruploading 60%\ruploaded", "carriage returned\r", "first of two\r\nsecond of two"} {
		deploy.Forward(&progressv1.OperationEvent{Message: message, Body: &progressv1.OperationEvent_Output{Output: &progressv1.Output{}}})
	}
	deploy.Forward(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Error: "retrying 1\rgave up"}}})

	got := sink.received()[1:]
	want := []string{"uploaded", "carriage returned", "first of two\nsecond of two"}
	for i, message := range want {
		if got[i].GetMessage() != message {
			t.Errorf("line %d reached the sink as %q, want %q, the last thing it said", i, got[i].GetMessage(), message)
		}
	}
	if outcome := got[3].GetResult().GetError(); outcome != "gave up" {
		t.Errorf("the outcome's error reached the sink as %q, want the rewrite collapsed wherever it sits", outcome)
	}
}

func TestAForwardedSpanThatEndsBeforeItStartedEndsWhenItArrives(t *testing.T) {
	sink := &recording{}
	run, c := begin(t, sink)
	started := c.read().Add(-time.Minute)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{
		Time:   timestamppb.New(started.Add(-time.Minute)),
		SpanId: []byte("unit-web"),
		Body:   &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{StartTimeUnixNano: started.UnixNano()}},
	})

	if at := sink.received()[1].GetTime().AsTime(); !at.Equal(c.read()) {
		t.Errorf("the span ended at %s, want the time it reached the bus, %s, not before it started", at, c.read())
	}
}

func TestTheAppsAProvidersOutcomeReportsAreOnTheRunsResult(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{
		Result: &progressv1.OperationResult{Apps: []*progressv1.AppResult{{App: "web"}}},
	}})
	var err error
	run.End(&err)

	got := sink.received()
	if got[1].GetResult() == nil {
		t.Fatalf("the provider's result reached the sinks as %v, want an outcome", bodies(got[1:2]))
	}
	if apps := got[len(got)-1].GetSummary().GetApps(); len(apps) != 1 || apps[0].GetApp() != "web" {
		t.Fatalf("result apps = %v, want web", apps)
	}
}

func TestARunInAProjectTracesItselfAndPointsItsResultAtItsLog(t *testing.T) {
	sink := &recording{}
	bus := run.NewBus(time.Now)
	bus.Attach(sink)
	dir := t.TempDir()

	_, run, err := bus.Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	run.End(&err)

	logPath := sink.received()[0].GetSummary().GetLogPath()
	if logPath != runFile(t, dir, ".ndjson") {
		t.Fatalf("log path = %q, want the trace's log under the project", logPath)
	}
	spans, readErr := os.ReadFile(strings.TrimSuffix(logPath, ".ndjson") + ".otlp.json")
	if readErr != nil || !strings.Contains(string(spans), "ocel deploy") {
		t.Fatalf("trace file = %q (%v), want the run's root span", spans, readErr)
	}
}

func TestARunInAProjectLogsEveryEventDebugIncludedUpToItsResultAndNothingAfter(t *testing.T) {
	bus := run.NewBus(time.Now)
	bus.Attach(&recording{})
	dir := t.TempDir()
	_, run, err := bus.Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	logPath := runFile(t, dir, ".ndjson")
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Debug("engine chatter")
	build.End(nil)
	run.End(&err)
	_, next, err := bus.Begin(context.Background(), "ocel env ls", "")
	if err != nil {
		t.Fatal(err)
	}
	next.Phase(progressv1.Phase_PHASE_CHECK).Say("after the run")

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("log line %q is not a run event: %v", line, err)
		}
		got = append(got, bodies([]*streamv1.RunEvent{ev})[0]+" "+ev.GetLevel().String())
	}
	want := []string{"started LEVEL_INFO", "message LEVEL_DEBUG", "ended LEVEL_INFO", "summary LEVEL_INFO"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the run's log = %q, want %q", got, want)
	}
}

func TestAFailedOrCancelledRunsHeadlineNamesTheCommandItEnded(t *testing.T) {
	for _, tc := range []struct {
		command     string
		interrupted bool
		want        string
	}{
		{"ocel deploy", false, "Deploy failed"},
		{"ocel preview up", false, "Preview up failed"},
		{"ocel bootstrap production", false, "Bootstrap production failed"},
		{"ocel deploy", true, "Deploy cancelled"},
		{"ocel domain use", true, "Domain use cancelled"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			sink := &recording{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bus := run.NewBus(newClock().read)
			bus.Attach(sink)
			_, run, err := bus.Begin(ctx, tc.command, "")
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("the upload was refused")
			if tc.interrupted {
				cancel()
				failure = context.Canceled
			}
			run.End(&failure)

			if got := sink.received()[len(sink.received())-1].GetSummary().GetHeadline(); got != tc.want {
				t.Errorf("`%s` ended with headline %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestASuccessfulRunThatReportedNoHeadlineIsHeadedByTheCommandItFinished(t *testing.T) {
	sink := &recording{}
	bus := run.NewBus(newClock().read)
	bus.Attach(sink)
	_, run, err := bus.Begin(context.Background(), "ocel env ls", "")
	if err != nil {
		t.Fatal(err)
	}
	run.End(&err)

	if got := sink.received()[len(sink.received())-1].GetSummary().GetHeadline(); got != "Env ls finished" {
		t.Errorf("the run ended with headline %q, want Env ls finished", got)
	}
}
