package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEveryScopeARunOpensEndsExactlyOnceBeforeItsResultWhenTheRunFails(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	check.Unit("aws", "Checking credentials").End(nil)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Unit("web", "Deploying web")
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
				t.Fatal("a scope ended after the run's result")
			}
		}
	}
	for id := range opened {
		if ended[id] != 1 {
			t.Fatalf("scope %s ended %d times, want once", id, ended[id])
		}
	}
	if strings.Join(order, ",") != "aws,,web," {
		t.Fatalf("ended subjects in order = %q, want aws, check, then web before its deploy phase", order)
	}
	web := got[len(got)-3]
	if web.GetSubject() != "web" || web.GetLevel() != progressv1.Level_LEVEL_ERROR || web.GetMessage() != "the upload was refused" {
		t.Fatalf("the open unit ended with level %s message %q, want the run's error", web.GetLevel(), web.GetMessage())
	}
}

func TestAFailedRunEndsWithAnErrorResultAndExitsWithOne(t *testing.T) {
	sink := &recording{}
	run, clock := begin(t, sink)
	clock.advance(1500 * time.Millisecond)

	err := errors.New("the upload was refused")
	run.End(&err)

	ev := sink.received()[len(sink.received())-1]
	result := ev.GetResult()
	if result.GetSuccess() || result.GetDetail() != "the upload was refused" || ev.GetLevel() != progressv1.Level_LEVEL_ERROR {
		t.Fatalf("result = success %v detail %q level %s, want a failure carrying the error at ERROR",
			result.GetSuccess(), result.GetDetail(), ev.GetLevel())
	}
	if result.GetDurationMs() != 1500 {
		t.Fatalf("duration = %dms, want 1500ms", result.GetDurationMs())
	}
	var exit *exitsig.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("err = %v, want an exit with code 1", err)
	}
}

func TestAFailedRunForMissingVariablesCarriesThemOnItsResult(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	var err error = &envgate.Refusal{Problems: []*resourcesv1.VariableProblem{{
		Key:  "DATABASE_URL",
		Kind: resourcesv1.VariableProblem_KIND_MISSING,
	}}}
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetResult()
	if cells := result.GetMissing().GetCells(); len(cells) != 1 || cells[0].GetKey() != "DATABASE_URL" {
		t.Fatalf("missing = %v, want DATABASE_URL", cells)
	}
	if result.GetDetail() != "" {
		t.Fatalf("detail = %q, want nothing beyond the missing variables", result.GetDetail())
	}
}

func TestAnInterruptedRunEndsCancelledAtWarnAndExitsAsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name string
		held bool
		note string
	}{
		{name: "mid-deploy", note: "Resources may be partially created."},
		{name: "while held", held: true, note: "Nothing has been provisioned."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recording{}
			ctx, cancel := context.WithCancel(context.Background())
			run, _ := beginIn(t, ctx, sink)
			if tc.held {
				run.Phase(progressv1.Phase_PHASE_BUILD).Hold(&streamv1.WaitingEvent{})
			}

			cancel()
			err := context.Canceled
			run.End(&err)

			ev := sink.received()[len(sink.received())-1]
			want := tc.note + "\nRe-run `ocel deploy` to reconcile."
			if !ev.GetResult().GetInterrupted() || ev.GetResult().GetHeadline() != "Cancelled" || ev.GetResult().GetDetail() != want ||
				ev.GetLevel() != progressv1.Level_LEVEL_WARN {
				t.Fatalf("result = interrupted %v headline %q detail %q level %s, want Cancelled at WARN with %q",
					ev.GetResult().GetInterrupted(), ev.GetResult().GetHeadline(), ev.GetResult().GetDetail(), ev.GetLevel(), want)
			}
			var exit *exitsig.ExitError
			if !errors.As(err, &exit) || exit.Code != exitsig.InterruptCode {
				t.Fatalf("err = %v, want the interrupt exit", err)
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
	if ev := got[len(got)-1]; !ev.GetResult().GetSuccess() || ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		t.Fatalf("result = success %v level %s, want success at INFO", ev.GetResult().GetSuccess(), ev.GetLevel())
	}
	if ended := got[len(got)-2]; ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Fatalf("the deploy phase ended %s, want OK", ended.GetEnded().GetStatus())
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !bytes.Equal(got[len(got)-2].GetSpanId(), got[0].GetSpanId()) {
		t.Fatal("the run ended some other scope than its deploy phase")
	}
}

func TestADeployedRunsSuccessResultCarriesItsHeadlineURLNotesAndFlipBound(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	flip := &progressv1.FlipBound{TypicalMs: 3000, Published: true}
	run.Deployed("Deployed", []string{"web: https://web.example.com"}, flip)

	var err error
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetResult()
	if !result.GetSuccess() || result.GetHeadline() != "Deployed" ||
		strings.Join(result.GetUrlNotes(), ",") != "web: https://web.example.com" || result.GetFlipBound().GetTypicalMs() != 3000 {
		t.Fatalf("result = %s, want a success headed Deployed with its url notes and flip bound", protojson.Format(result))
	}
}

func TestARunThatFailsAfterReportingAHeadlineEndsWithTheFailureAlone(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	run.Finish("Nothing to deploy")

	err := errors.New("the service map could not be written")
	run.End(&err)

	result := sink.received()[len(sink.received())-1].GetResult()
	if result.GetSuccess() || result.GetHeadline() != "" || result.GetDetail() != "the service map could not be written" {
		t.Fatalf("result = %s, want the failure with no success headline", protojson.Format(result))
	}
}

func TestAForwardedProviderEventReachesTheSinksAsARunEventWithItsEnvelope(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	at := time.Date(2026, 9, 27, 12, 0, 5, 0, time.UTC)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{
		TimeUnixNano: at.UnixNano(),
		Level:        progressv1.Level_LEVEL_WARN,
		Phase:        progressv1.Phase_PHASE_PROVISION,
		Subject:      "web",
		Message:      "Provisioning web",
		SpanId:       []byte("unit-web"),
		Body:         &progressv1.OperationEvent_Started{Started: &progressv1.Started{ParentSpanId: []byte("phase-01")}},
	})

	ev := sink.received()[1]
	if !ev.GetTime().AsTime().Equal(at) || ev.GetLevel() != progressv1.Level_LEVEL_WARN || ev.GetPhase() != progressv1.Phase_PHASE_PROVISION ||
		ev.GetSubject() != "web" || ev.GetMessage() != "Provisioning web" || string(ev.GetSpanId()) != "unit-web" ||
		string(ev.GetStarted().GetParentSpanId()) != "phase-01" {
		t.Fatalf("forwarded = time %s level %s phase %s subject %q message %q span %q parent %q, want the provider's envelope and body",
			ev.GetTime().AsTime(), ev.GetLevel(), ev.GetPhase(), ev.GetSubject(), ev.GetMessage(), ev.GetSpanId(), ev.GetStarted().GetParentSpanId())
	}
}

func TestTheAppsAProvidersOutcomeReportsAreOnTheRunsResult(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{
		Result: &progressv1.ResultEvent{Apps: []*progressv1.AppResult{{App: "web"}}},
	}})
	var err error
	run.End(&err)

	got := sink.received()
	if got[1].GetOutcome() == nil {
		t.Fatalf("the provider's result reached the sinks as %v, want an outcome", bodies(got[1:2]))
	}
	if apps := got[len(got)-1].GetResult().GetApps(); len(apps) != 1 || apps[0].GetApp() != "web" {
		t.Fatalf("result apps = %v, want web", apps)
	}
}

func TestARunInAProjectTracesItselfAndPointsItsResultAtItsLog(t *testing.T) {
	sink := &recording{}
	bus := events.NewBus(time.Now)
	bus.Attach(sink)
	dir := t.TempDir()

	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	traced := runtrace.FromContext(ctx)
	if traced == nil {
		t.Fatal("the run's context carries no trace")
	}
	run.End(&err)

	logPath := sink.received()[0].GetResult().GetLogPath()
	if logPath != traced.LogPath() || !strings.HasPrefix(logPath, filepath.Join(dir, constants.ProjectStateDirName, "runs")) {
		t.Fatalf("log path = %q, want the trace's log %q under the project", logPath, traced.LogPath())
	}
	spans, readErr := os.ReadFile(filepath.Join(filepath.Dir(logPath), traced.TraceID()+".otlp.json"))
	if readErr != nil || !strings.Contains(string(spans), "ocel deploy") {
		t.Fatalf("trace file = %q (%v), want the run's root span", spans, readErr)
	}
}

func TestARunInAProjectLogsEveryEventDebugIncludedUpToItsResultAndNothingAfter(t *testing.T) {
	bus := events.NewBus(time.Now)
	bus.Attach(&recording{})
	ctx, run, err := bus.Begin(context.Background(), "ocel deploy", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := runtrace.FromContext(ctx).LogPath()
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Debug("engine chatter")
	build.End(nil)
	run.End(&err)
	bus.Send(&streamv1.RunEvent{Message: "after the run"})

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
	want := []string{"started LEVEL_INFO", "message LEVEL_DEBUG", "ended LEVEL_INFO", "result LEVEL_INFO"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the run's log = %q, want %q", got, want)
	}
}
