package events_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAUnitsEventsCarryItsPhaseSubjectAndSpanAndItsParentIsThePhaseScope(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	web.Say("Bundling")
	web.Debug("esbuild 0.25")
	web.Error("the bundle is too large")

	got := sink.received()
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	phase, unit := got[0], got[1]
	if !bytes.Equal(unit.GetStarted().GetParentSpanId(), phase.GetSpanId()) || len(phase.GetSpanId()) != 8 {
		t.Fatalf("unit parent = %x, want the phase span %x", unit.GetStarted().GetParentSpanId(), phase.GetSpanId())
	}
	if unit.GetMessage() != "Building web" {
		t.Fatalf("unit started message = %q, want %q", unit.GetMessage(), "Building web")
	}
	levels := []progressv1.Level{progressv1.Level_LEVEL_INFO, progressv1.Level_LEVEL_DEBUG, progressv1.Level_LEVEL_ERROR}
	for i, ev := range got[1:] {
		if ev.GetPhase() != progressv1.Phase_PHASE_BUILD || ev.GetSubject() != "web" || !bytes.Equal(ev.GetSpanId(), unit.GetSpanId()) {
			t.Fatalf("event %d = phase %s subject %q span %x, want the build phase, web and the unit's span %x",
				i+1, ev.GetPhase(), ev.GetSubject(), ev.GetSpanId(), unit.GetSpanId())
		}
		if i > 0 && ev.GetLevel() != levels[i-1] {
			t.Fatalf("event %d level = %s, want %s", i+1, ev.GetLevel(), levels[i-1])
		}
	}
	if bytes.Equal(unit.GetSpanId(), phase.GetSpanId()) {
		t.Fatal("the unit shares its phase's span id")
	}
}

func TestEndReportsTheErrorOnTheEndedEventAndTheDurationFromItsStart(t *testing.T) {
	sink := &recording{}
	run, clock := begin(t, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", "Deploying web")
	started := clock.read()

	clock.advance(3 * time.Second)
	web.End(errors.New("the upload was refused"))

	ended := sink.received()[2]
	if ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR || ended.GetLevel() != progressv1.Level_LEVEL_ERROR {
		t.Fatalf("ended = status %s level %s, want an error", ended.GetEnded().GetStatus(), ended.GetLevel())
	}
	if ended.GetMessage() != "the upload was refused" {
		t.Fatalf("ended message = %q, want the error", ended.GetMessage())
	}
	if !bytes.Equal(ended.GetSpanId(), sink.received()[1].GetSpanId()) || ended.GetSubject() != "web" {
		t.Fatalf("ended span %x subject %q, want the unit's", ended.GetSpanId(), ended.GetSubject())
	}
	took := ended.GetTime().AsTime().Sub(time.Unix(0, ended.GetEnded().GetStartTimeUnixNano()))
	if !started.Equal(time.Unix(0, ended.GetEnded().GetStartTimeUnixNano())) || took != 3*time.Second {
		t.Fatalf("ended start %d took %s, want %s and 3s", ended.GetEnded().GetStartTimeUnixNano(), took, started)
	}
}

func TestAScopeThatSucceedsEndsOnceWithAnOKStatus(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", "Deploying web")

	web.End(nil)
	web.End(errors.New("too late"))

	got := sink.received()
	if len(got) != 3 {
		t.Fatalf("got %d events, want started, started, ended", len(got))
	}
	if got[2].GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK || got[2].GetLevel() != progressv1.Level_LEVEL_INFO || got[2].GetMessage() != "" {
		t.Fatalf("ended = status %s level %s message %q, want OK, INFO and no message",
			got[2].GetEnded().GetStatus(), got[2].GetLevel(), got[2].GetMessage())
	}
}

func TestAScopeEndedByAnInterruptIsAWarningNotAnError(t *testing.T) {
	sink := &recording{}
	ctx, cancel := context.WithCancel(context.Background())
	run, _ := beginIn(t, ctx, sink)
	web := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", "Deploying web")

	cancel()
	web.End(context.Canceled)

	ended := sink.received()[2]
	if ended.GetLevel() != progressv1.Level_LEVEL_WARN || ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("ended = level %s status %s, want WARN and an error status", ended.GetLevel(), ended.GetEnded().GetStatus())
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
	if got[0].GetPhase() != progressv1.Phase_PHASE_BUILD || got[2].GetPhase() != progressv1.Phase_PHASE_BUILD {
		t.Fatalf("hold phases = %s and %s, want the scope's", got[0].GetPhase(), got[2].GetPhase())
	}
}

func TestARunHeldOutsideAnyScopeWaitsAndResumesInNoPhaseAndOnNoSpan(t *testing.T) {
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
		if ev.GetPhase() != progressv1.Phase_PHASE_UNSPECIFIED || len(ev.GetSpanId()) != 0 {
			t.Errorf("%v arrived in %s on span %x, want the run's, in no phase and on no span", bodies([]*streamv1.RunEvent{ev}), ev.GetPhase(), ev.GetSpanId())
		}
	}
}

func bodies(evs []*streamv1.RunEvent) []string {
	var out []string
	for _, ev := range evs {
		name := "message"
		if body := ev.ProtoReflect().WhichOneof(ev.ProtoReflect().Descriptor().Oneofs().ByName("body")); body != nil {
			name = string(body.Name())
		}
		out = append(out, name)
	}
	return out
}

func TestAPlanIsDrawnWithItsHeadlineAndNotesInItsScopesPhaseLeavingTheCallersPlanUntouched(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	plan := &planv1.ChangePlan{Headline: "from the provider"}

	drawn := run.Phase(progressv1.Phase_PHASE_PLAN).Plan("Deploy to production", plan, "2 resources change")

	ev := sink.received()[1]
	if !proto.Equal(ev.GetPlan(), drawn) || drawn.GetHeadline() != "Deploy to production" || len(drawn.GetNotes()) != 1 ||
		ev.GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("plan event = %q notes %q phase %s, want the drawn plan in the plan phase", drawn.GetHeadline(), drawn.GetNotes(), ev.GetPhase())
	}
	if plan.GetHeadline() != "from the provider" {
		t.Fatalf("the caller's plan headline became %q", plan.GetHeadline())
	}
}

func TestThePlanAScopeDrawsIsThePlanEverySinkShowsInSpineOrder(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	plan := &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "edge", Name: "cloudflare/edge", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: planv1.Change_ACTION_UPDATE, Changes: []*planv1.Change{
			{Kind: "b", Name: "second", Action: planv1.Change_ACTION_UPDATE},
			{Kind: "a", Name: "first", Action: planv1.Change_ACTION_CREATE},
		}},
	}}

	drawn := run.Phase(progressv1.Phase_PHASE_PLAN).Plan("Proposed changes", plan)

	shown := sink.received()[1].GetPlan()
	if !proto.Equal(drawn, shown) {
		t.Fatalf("the plan the scope hands back is\n%v\nand the plan the sinks show is\n%v", drawn, shown)
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
	return sink.received()[1].GetPlan()
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

func TestPlanGroupsReachEverySinkInSpineOrderWhateverOrderTheyArriveIn(t *testing.T) {
	spine := []*planv1.ChangeGroup{
		{Kind: "stack", Name: "aws/ocel-production-core"},
		{Kind: "parameters", Name: "aws/parameters"},
		{Kind: "stack", Name: "aws/shop--web--b1"},
		{Kind: "stack", Name: "aws/shop--api--b1"},
		{Kind: "edge", Name: "cloudfront/edge"},
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

	want := "stack/aws/ocel-production-core parameters/aws/parameters stack/aws/shop--web--b1 " +
		"stack/aws/shop--api--b1 edge/cloudfront/edge certificate/ocels-cert DNS record/shop.example " +
		"variable values/shop stored objects/shop"
	if names := groupNames(drawn(t, arrived...)); names != want {
		t.Errorf("group order = %q, want %q — infra and apps in the order the plan names them, then edge, then what sits outside the spine", names, want)
	}
	if names := groupNames(drawn(t, spine...)); names != want {
		t.Errorf("group order = %q for a plan that arrived in spine order already, want %q", names, want)
	}
}

func TestIdentityReachesTheSinksInItsScopesPhase(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)

	run.Phase(progressv1.Phase_PHASE_CHECK).Identity(&streamv1.IdentityEvent{Project: "shop"})

	if ev := sink.received()[1]; ev.GetIdentity().GetProject() != "shop" || ev.GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("identity = %q in %s, want shop in the check phase", ev.GetIdentity().GetProject(), ev.GetPhase())
	}
}

func TestEndingAPhaseEndsItsOpenUnitsFirstWithTheSameError(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	deploy := run.Phase(progressv1.Phase_PHASE_DEPLOY)
	deploy.Unit("web", "Deploying web").Unit("web", "Uploading web")
	deploy.Unit("api", "Deploying api").End(nil)

	deploy.End(errors.New("the stack is locked"))

	opened, got := sink.received()[:5], sink.received()[5:]
	want := [][]byte{opened[2].GetSpanId(), opened[1].GetSpanId(), opened[0].GetSpanId()}
	if len(got) != len(want) {
		t.Fatalf("got %v after the api unit ended, want three ended events", bodies(got))
	}
	for i, ev := range got {
		if !bytes.Equal(ev.GetSpanId(), want[i]) || ev.GetEnded() == nil || ev.GetMessage() != "the stack is locked" {
			t.Fatalf("ended %d = span %x message %q, want span %x with the phase's error", i, ev.GetSpanId(), ev.GetMessage(), want[i])
		}
	}
}
