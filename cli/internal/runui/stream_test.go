package runui

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func recorded(t *testing.T, events ...*streamv1.RunEvent) []*streamv1.RunEvent {
	t.Helper()
	var out safeBuffer
	s := NewStream(&out, Presentation{Format: FormatJSON, Width: defaultWidth})
	for _, ev := range events {
		s.Emit(ev)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return parseNDJSON(t, out.String())
}

func parseNDJSON(t *testing.T, raw string) []*streamv1.RunEvent {
	t.Helper()
	var out []*streamv1.RunEvent
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestCarriageReturnRewritesCollapseOnIngest(t *testing.T) {
	t.Parallel()

	got := recorded(t, operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{
			StageId: appStage(1),
			Message: "downloading 10%\rdownloading 60%\rdownloading 100%",
		},
	}}))

	if len(got) != 1 {
		t.Fatalf("recorded %d envelopes, want 1", len(got))
	}
	if want := "downloading 100%"; got[0].GetOperation().GetProgress().GetMessage() != want {
		t.Errorf("recorded message = %q, want %q — rewrites collapse before anything projects them",
			got[0].GetOperation().GetProgress().GetMessage(), want)
	}
}

func TestPlanRowsReachTheStreamInOneOrderWhateverOrderTheyArriveIn(t *testing.T) {
	t.Parallel()

	rows := []*planv1.Change{
		{Kind: "bucket", Name: "assets", Action: planv1.Change_ACTION_CREATE},
		{Kind: "function", Name: "api", Action: planv1.Change_ACTION_UPDATE},
		{Kind: "bucket", Name: "logs", Action: planv1.Change_ACTION_DELETE},
	}
	shuffled := []*planv1.Change{rows[2], rows[0], rows[1]}

	first := recorded(t, plan(rows))
	second := recorded(t, plan(shuffled))

	if a, b := planNames(first[0]), planNames(second[0]); a != b {
		t.Errorf("row order = %q for one arrival order and %q for another, want one order for one plan", a, b)
	}
	if want := "bucket/assets bucket/logs function/api"; planNames(first[0]) != want {
		t.Errorf("row order = %q, want %q", planNames(first[0]), want)
	}
}

func TestPlanGroupsReachTheStreamInSpineOrderWhateverOrderTheyArriveIn(t *testing.T) {
	t.Parallel()

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

	want := "stack/aws/ocel-production-core parameters/aws/parameters stack/aws/shop--web--b1 " +
		"stack/aws/shop--api--b1 edge/cloudfront/edge certificate/ocels-cert DNS record/shop.example " +
		"variable values/shop stored objects/shop"
	if names := groupNames(recorded(t, planOf(arrived))[0]); names != want {
		t.Errorf("group order = %q, want %q — infra and apps in the order the plan names them, then edge, then what sits outside the spine", names, want)
	}
	if names := groupNames(recorded(t, planOf(spine))[0]); names != want {
		t.Errorf("group order = %q for a plan that arrived in spine order already, want %q", names, want)
	}
}

func planOf(groups []*planv1.ChangeGroup) *streamv1.RunEvent {
	return &streamv1.RunEvent{Event: &streamv1.RunEvent_Plan{Plan: &planv1.ChangePlan{
		Subject: "production",
		Groups:  groups,
	}}}
}

func groupNames(ev *streamv1.RunEvent) string {
	var names []string
	for _, g := range ev.GetPlan().GetGroups() {
		names = append(names, g.GetKind()+"/"+g.GetName())
	}
	return strings.Join(names, " ")
}

func plan(changes []*planv1.Change) *streamv1.RunEvent {
	return &streamv1.RunEvent{Event: &streamv1.RunEvent_Plan{Plan: &planv1.ChangePlan{
		Subject: "production",
		Groups:  []*planv1.ChangeGroup{{Kind: "app", Name: "web", Changes: changes}},
	}}}
}

func planNames(ev *streamv1.RunEvent) string {
	var names []string
	for _, g := range ev.GetPlan().GetGroups() {
		for _, c := range g.GetChanges() {
			names = append(names, c.GetKind()+"/"+c.GetName())
		}
	}
	return strings.Join(names, " ")
}

func TestNDJSONIsOneEnvelopePerLineAndNeverBuffers(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	s := NewStream(&out, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Emit(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
		Stages: []*progressv1.Stage{{Id: appStage(1), Title: "web"}},
	}}}))

	if lines := strings.Count(out.String(), "\n"); lines != 1 {
		t.Errorf("after one envelope the stream holds %d lines, want 1 — the machine surface is the off-TTY liveness surface", lines)
	}
	if strings.Contains(out.String(), "\n ") || strings.Contains(strings.TrimRight(out.String(), "\n"), "\n") {
		t.Errorf("stream = %q, want exactly one line per envelope", out.String())
	}
}
