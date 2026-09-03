package runui

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func providerResult(success bool, apps ...*progressv1.AppResult) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{
		Result: &progressv1.ResultEvent{Success: success, Apps: apps},
	}}
}

func appOutcomesOf(t *testing.T, raw string) []string {
	t.Helper()
	var result *streamv1.RunResultEvent
	for _, ev := range parseNDJSON(t, raw) {
		if held := ev.GetResult(); held != nil {
			result = held
		}
	}
	if result == nil {
		t.Fatalf("the stream %q carries no run result envelope", raw)
	}
	reported := make([]string, 0, len(result.GetApps()))
	for _, app := range result.GetApps() {
		reported = append(reported, app.GetApp()+"="+app.GetOutcome().String())
	}
	return reported
}

func TestRunResultCarriesTheProviderAppOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		apps   []*progressv1.AppResult
		finish func(*Session)
		want   []string
	}{
		{
			name: "a deploy where every app stood up",
			apps: []*progressv1.AppResult{
				{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
				{App: "admin", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
			},
			finish: func(s *Session) { s.Deployed("Deployed", "", Flip{}, nil, nil) },
			want:   []string{"web=APP_OUTCOME_SUCCEEDED", "admin=APP_OUTCOME_SUCCEEDED"},
		},
		{
			name: "a deploy one of whose apps failed",
			apps: []*progressv1.AppResult{
				{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED, Error: "the web stack would not stand up"},
				{App: "admin", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
			},
			finish: func(s *Session) { s.Fail(errors.New("the web stack would not stand up")) },
			want:   []string{"web=APP_OUTCOME_FAILED", "admin=APP_OUTCOME_SUCCEEDED"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			run := startTestRun(t, dir, "ocel deploy")
			var out bytes.Buffer
			s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
			t.Cleanup(func() { _ = s.Close() })

			s.Event(providerResult(false, tc.apps...))
			tc.finish(s)

			if got := appOutcomesOf(t, out.String()); !slices.Equal(got, tc.want) {
				t.Fatalf("the run result reports %v, want %v", got, tc.want)
			}
		})
	}
}
