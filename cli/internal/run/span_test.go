package run_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAUnitsEventsCarryItsPhaseSubjectAndSpanAndItsParentIsThePhaseSpan(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", progress.Building.Title("web"))
	web.Say("Bundling")
	web.Debug("esbuild 0.25")
	web.Error("the bundle is too large")

	got := sink.received()
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	phase, unit := got[0], got[1]
	if !bytes.Equal(unit.GetOperation().GetStarted().GetParentSpanId(), phase.GetOperation().GetSpanId()) || len(phase.GetOperation().GetSpanId()) != 8 {
		t.Fatalf("unit parent = %x, want the phase span %x", unit.GetOperation().GetStarted().GetParentSpanId(), phase.GetOperation().GetSpanId())
	}
	if unit.GetOperation().GetMessage() != "Building web" {
		t.Fatalf("unit started message = %q, want %q", unit.GetOperation().GetMessage(), "Building web")
	}
	levels := []progressv1.Level{progressv1.Level_LEVEL_INFO, progressv1.Level_LEVEL_DEBUG, progressv1.Level_LEVEL_ERROR}
	for i, ev := range got[1:] {
		if ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD || ev.GetOperation().GetSubject() != "web" || !bytes.Equal(ev.GetOperation().GetSpanId(), unit.GetOperation().GetSpanId()) {
			t.Fatalf("event %d = phase %s subject %q span %x, want the build phase, web and the unit's span %x",
				i+1, ev.GetOperation().GetPhase(), ev.GetOperation().GetSubject(), ev.GetOperation().GetSpanId(), unit.GetOperation().GetSpanId())
		}
		if i > 0 && ev.GetOperation().GetLevel() != levels[i-1] {
			t.Fatalf("event %d level = %s, want %s", i+1, ev.GetOperation().GetLevel(), levels[i-1])
		}
	}
	if bytes.Equal(unit.GetOperation().GetSpanId(), phase.GetOperation().GetSpanId()) {
		t.Fatal("the unit shares its phase's span id")
	}
}

func TestAUnitThatSucceedsEndsTitledWithWhatItDidAndOneThatFailsWithNoTitle(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)

	build.Unit("web", progress.Building.Title("web")).End(nil)
	build.Unit("api", progress.Building.Title("api")).End(errors.New("the bundle is too large"))

	var titles []string
	for _, ev := range sink.received() {
		if ended := ev.GetOperation().GetEnded(); ended != nil && ev.GetOperation().GetSubject() != "" {
			titles = append(titles, ended.GetTitle())
		}
	}
	if want := []string{"Built web", ""}; strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("ended titles = %q, want %q", titles, want)
	}
}

func TestEndReportsTheErrorOnTheEndedEventAndTheDurationFromItsStart(t *testing.T) {
	sink := &recording{}
	run, clock := begin(t, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", progress.Deploying.Title("web"))
	started := clock.read()

	clock.advance(3 * time.Second)
	web.End(errors.New("the upload was refused"))

	ended := sink.received()[2]
	if ended.GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR || ended.GetOperation().GetLevel() != progressv1.Level_LEVEL_ERROR {
		t.Fatalf("ended = status %s level %s, want an error", ended.GetOperation().GetEnded().GetStatus(), ended.GetOperation().GetLevel())
	}
	if ended.GetOperation().GetMessage() != "the upload was refused" {
		t.Fatalf("ended message = %q, want the error", ended.GetOperation().GetMessage())
	}
	if !bytes.Equal(ended.GetOperation().GetSpanId(), sink.received()[1].GetOperation().GetSpanId()) || ended.GetOperation().GetSubject() != "web" {
		t.Fatalf("ended span %x subject %q, want the unit's", ended.GetOperation().GetSpanId(), ended.GetOperation().GetSubject())
	}
	took := ended.GetOperation().GetTime().AsTime().Sub(time.Unix(0, ended.GetOperation().GetEnded().GetStartTimeUnixNano()))
	if !started.Equal(time.Unix(0, ended.GetOperation().GetEnded().GetStartTimeUnixNano())) || took != 3*time.Second {
		t.Fatalf("ended start %d took %s, want %s and 3s", ended.GetOperation().GetEnded().GetStartTimeUnixNano(), took, started)
	}
}

func TestAReservedUnitAnnouncesNothingUntilOpenedAndThenStartsWhenItWasReserved(t *testing.T) {
	sink := &recording{}
	run, clock := begin(t, sink)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	reserved := build.ReserveUnit("shop", progress.Building.Title("2 apps (web and api)"))
	reservedAt := clock.read()
	before := len(sink.received())

	clock.advance(4 * time.Second)
	if got := len(sink.received()); got != before {
		t.Fatalf("reserving a unit sent %d events, want none", got-before)
	}
	unit := reserved.Open()
	unit.End(errors.New("node not found on PATH"))

	got := sink.received()[before:]
	if len(got) != 2 {
		t.Fatalf("opening and ending the unit sent %d events, want 2", len(got))
	}
	if got[0].GetOperation().GetMessage() != "Building 2 apps (web and api)" || got[0].GetOperation().GetSubject() != "shop" {
		t.Fatalf("the unit opened as %s: %q, want shop: %q", got[0].GetOperation().GetSubject(), got[0].GetOperation().GetMessage(), "Building 2 apps (web and api)")
	}
	if !got[0].GetOperation().GetTime().AsTime().Equal(reservedAt) || got[1].GetOperation().GetEnded().GetStartTimeUnixNano() != reservedAt.UnixNano() {
		t.Errorf("the unit started at %s, want %s: when it was reserved", got[0].GetOperation().GetTime().AsTime(), reservedAt)
	}
	if took := got[1].GetOperation().GetTime().AsTime().Sub(reservedAt); took != 4*time.Second {
		t.Errorf("the unit took %s, want 4s", took)
	}
}

func TestASpanThatSucceedsEndsOnceWithAnOKStatus(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", progress.Deploying.Title("web"))

	web.End(nil)
	web.End(errors.New("too late"))

	got := sink.received()
	if len(got) != 3 {
		t.Fatalf("got %d events, want started, started, ended", len(got))
	}
	if got[2].GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK || got[2].GetOperation().GetLevel() != progressv1.Level_LEVEL_INFO || got[2].GetOperation().GetMessage() != "" {
		t.Fatalf("ended = status %s level %s message %q, want OK, INFO and no message",
			got[2].GetOperation().GetEnded().GetStatus(), got[2].GetOperation().GetLevel(), got[2].GetOperation().GetMessage())
	}
}

func TestASpanEndedByAnInterruptIsAWarningNotAnError(t *testing.T) {
	sink := &recording{}
	ctx, cancel := context.WithCancel(context.Background())
	run, _ := beginIn(t, ctx, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", progress.Deploying.Title("web"))

	cancel()
	web.End(context.Canceled)

	ended := sink.received()[2]
	if ended.GetOperation().GetLevel() != progressv1.Level_LEVEL_WARN || ended.GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("ended = level %s status %s, want WARN and an error status", ended.GetOperation().GetLevel(), ended.GetOperation().GetEnded().GetStatus())
	}
}

func TestHoldEmitsWaitingThenResumedAroundTheInteraction(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)

	resume := build.Hold(&streamv1.WaitingEvent{Url: "https://ocel.dev/vars#token"})
	build.Say("the page was answered")
	resume("the page was answered")

	got := sink.received()[1:]
	if len(got) != 3 || got[0].GetWaiting().GetUrl() != "https://ocel.dev/vars#token" || got[1].GetWaiting() != nil ||
		got[2].GetResumed().GetReason() != "the page was answered" {
		t.Fatalf("events = %v, want waiting, the message, then resumed", bodies(got))
	}
	if got[0].GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD || got[2].GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD {
		t.Fatalf("hold phases = %s and %s, want the span's", got[0].GetOperation().GetPhase(), got[2].GetOperation().GetPhase())
	}
}

func TestARunHeldOutsideAnySpanWaitsAndResumesInNoPhaseAndOnNoSpan(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	check.End(nil)

	run.Hold(&streamv1.WaitingEvent{})("answered")

	got := sink.received()[2:]
	if len(got) != 2 || got[0].GetWaiting() == nil || got[1].GetResumed().GetReason() != "answered" {
		t.Fatalf("events = %v, want waiting then resumed", bodies(got))
	}
	for _, ev := range got {
		if ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_UNSPECIFIED || len(ev.GetOperation().GetSpanId()) != 0 {
			t.Errorf("%v arrived in %s on span %x, want the run's, in no phase and on no span", bodies([]*streamv1.RunEvent{ev}), ev.GetOperation().GetPhase(), ev.GetOperation().GetSpanId())
		}
	}
}

func bodies(evs []*streamv1.RunEvent) []string {
	var out []string
	for _, ev := range evs {
		name := "message"
		if cli := ev.ProtoReflect().WhichOneof(ev.ProtoReflect().Descriptor().Oneofs().ByName("cli")); cli != nil {
			name = string(cli.Name())
		} else if op := ev.GetOperation(); op != nil {
			if body := op.ProtoReflect().WhichOneof(op.ProtoReflect().Descriptor().Oneofs().ByName("body")); body != nil {
				name = string(body.Name())
			}
		}
		out = append(out, name)
	}
	return out
}

func TestAPlanIsDrawnWithItsHeadlineAndNotesInItsSpansPhaseLeavingTheCallersPlanUntouched(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	plan := &planv1.ChangePlan{Headline: "from the provider"}

	drawn := run.Phase(progressv1.Phase_PHASE_PLAN).Plan("Deploy to production", plan, &planv1.Note{Text: "2 resources change"})

	ev := sink.received()[1]
	if !proto.Equal(ev.GetOperation().GetPlan(), drawn) || drawn.GetHeadline() != "Deploy to production" || len(drawn.GetNotes()) != 1 ||
		ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("plan event = %q notes %q phase %s, want the drawn plan in the plan phase", drawn.GetHeadline(), drawn.GetNotes(), ev.GetOperation().GetPhase())
	}
	if plan.GetHeadline() != "from the provider" {
		t.Fatalf("the caller's plan headline became %q", plan.GetHeadline())
	}
}

func TestThePlanASpanDrawsIsThePlanEverySinkShowsInGroupOrder(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	plan := &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "edge", Name: "relay/edge", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "fake/ocel-bootstrap", Action: planv1.Change_ACTION_UPDATE, Changes: []*planv1.Change{
			{Kind: "b", Name: "second", Action: planv1.Change_ACTION_UPDATE},
			{Kind: "a", Name: "first", Action: planv1.Change_ACTION_CREATE},
		}},
	}}

	drawn := run.Phase(progressv1.Phase_PHASE_PLAN).Plan("Proposed changes", plan)

	shown := sink.received()[1].GetOperation().GetPlan()
	if !proto.Equal(drawn, shown) {
		t.Fatalf("the plan the span hands back is\n%v\nand the plan the sinks show is\n%v", drawn, shown)
	}
	if first := drawn.GetGroups()[0].GetKind(); first != "stack" {
		t.Fatalf("the drawn plan opens on a %q group, want the spine order: stack before edge", first)
	}
	if first := drawn.GetGroups()[0].GetChanges()[0].GetName(); first != "first" {
		t.Errorf("the drawn plan's first row is %q, want rows in kind order", first)
	}
}

func drawn(t *testing.T, groups ...*planv1.ChangeGroup) *planv1.ChangePlan {
	t.Helper()
	sink := &recording{}
	run, _ := begin(t, sink)
	run.Phase(progressv1.Phase_PHASE_PLAN).Plan("Proposed changes", &planv1.ChangePlan{Subject: "production", Groups: groups})
	return sink.received()[1].GetOperation().GetPlan()
}

func TestPlanRowsReachEverySinkInOneOrderWhateverOrderTheyArriveIn(t *testing.T) {
	rows := []*planv1.Change{
		{Kind: "bucket", Name: "assets", Action: planv1.Change_ACTION_CREATE},
		{Kind: "function", Name: "api", Action: planv1.Change_ACTION_UPDATE},
		{Kind: "bucket", Name: "logs", Action: planv1.Change_ACTION_DELETE},
	}
	rowNames := func(plan *planv1.ChangePlan) string {
		var names []string
		for _, c := range plan.GetGroups()[0].GetChanges() {
			names = append(names, c.GetKind()+"/"+c.GetName())
		}
		return strings.Join(names, " ")
	}

	first := drawn(t, &planv1.ChangeGroup{Kind: "app", Name: "web", Changes: rows})
	second := drawn(t, &planv1.ChangeGroup{Kind: "app", Name: "web", Changes: []*planv1.Change{rows[2], rows[0], rows[1]}})

	if a, b := rowNames(first), rowNames(second); a != b {
		t.Errorf("row order = %q for one arrival order and %q for another, want one order for one plan", a, b)
	}
	if want := "bucket/assets bucket/logs function/api"; rowNames(first) != want {
		t.Errorf("row order = %q, want %q", rowNames(first), want)
	}
}

func TestPlanGroupsReachEverySinkInGroupOrderWhateverOrderTheyArriveIn(t *testing.T) {
	spine := []*planv1.ChangeGroup{
		{Kind: "stack", Name: "fake/ocel-production-core"},
		{Kind: "parameters", Name: "fake/parameters"},
		{Kind: "stack", Name: "fake/shop--web--b1"},
		{Kind: "stack", Name: "fake/shop--api--b1"},
		{Kind: "edge", Name: "direct/edge"},
		{Kind: "certificate", Name: "ocels-cert"},
		{Kind: "DNS record", Name: "shop.example"},
		{Kind: "variable values", Name: "shop"},
		{Kind: "stored objects", Name: "shop"},
	}
	arrived := []*planv1.ChangeGroup{
		spine[5], spine[4], spine[0], spine[6], spine[1], spine[2], spine[7], spine[3], spine[8],
	}
	groupNames := func(plan *planv1.ChangePlan) string {
		var names []string
		for _, g := range plan.GetGroups() {
			names = append(names, g.GetKind()+"/"+g.GetName())
		}
		return strings.Join(names, " ")
	}

	want := "stack/fake/ocel-production-core parameters/fake/parameters stack/fake/shop--web--b1 " +
		"stack/fake/shop--api--b1 edge/direct/edge certificate/ocels-cert DNS record/shop.example " +
		"variable values/shop stored objects/shop"
	if names := groupNames(drawn(t, arrived...)); names != want {
		t.Errorf("group order = %q, want %q — infra and apps in the order the plan names them, then edge, then what sits outside the spine", names, want)
	}
	if names := groupNames(drawn(t, spine...)); names != want {
		t.Errorf("group order = %q for a plan that arrived in spine order already, want %q", names, want)
	}
}

func TestIdentityReachesTheSinksInItsSpansPhase(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	run.Phase(progressv1.Phase_PHASE_CHECK).Identity(&streamv1.IdentityEvent{Project: "shop"})

	if ev := sink.received()[1]; ev.GetIdentity().GetProject() != "shop" || ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("identity = %q in %s, want shop in the check phase", ev.GetIdentity().GetProject(), ev.GetOperation().GetPhase())
	}
}

func TestEndingAPhaseEndsItsOpenUnitsFirstWithTheSameError(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Unit("web", progress.Deploying.Title("web")).Unit("web", progress.Title{Started: "Uploading web", Ended: "Uploading web"})
	deploy.Unit("api", progress.Deploying.Title("api")).End(nil)

	deploy.End(errors.New("the stack is locked"))

	opened, got := sink.received()[:5], sink.received()[5:]
	want := [][]byte{opened[2].GetOperation().GetSpanId(), opened[1].GetOperation().GetSpanId(), opened[0].GetOperation().GetSpanId()}
	if len(got) != len(want) {
		t.Fatalf("got %v after the api unit ended, want three ended events", bodies(got))
	}
	for i, ev := range got {
		if !bytes.Equal(ev.GetOperation().GetSpanId(), want[i]) || ev.GetOperation().GetEnded() == nil || ev.GetOperation().GetMessage() != "the stack is locked" {
			t.Fatalf("ended %d = span %x message %q, want span %x with the phase's error", i, ev.GetOperation().GetSpanId(), ev.GetOperation().GetMessage(), want[i])
		}
	}
}

func TestAForwardedProviderEventKeepsEveryFieldItCarried(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	op := &progressv1.OperationEvent{
		Time:    timestamppb.New(time.Unix(1790503200, 0)),
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_DEPLOY,
		Subject: "web",
		Message: "the certificate is still issuing",
		SpanId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
			Status: progressv1.SpanStatus_SPAN_STATUS_OK,
			Title:  "Attached production hostname www.shop.example",
		}},
	}

	run.Phase(progressv1.Phase_PHASE_DEPLOY).Forward(op)

	got := sink.received()[len(sink.received())-1]
	if want := (&streamv1.RunEvent{Operation: op}); !proto.Equal(got, want) {
		t.Errorf("the forwarded event is not span %s's end carried over field for field", op.GetSpanId())
	}
}
