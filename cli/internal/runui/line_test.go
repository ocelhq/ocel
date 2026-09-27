package runui

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAWarningReadsLevelPhaseSubjectAndMessage(t *testing.T) {
	got := line{
		level:   progressv1.Level_LEVEL_WARN,
		phase:   progressv1.Phase_PHASE_CHECK,
		subject: "cloudflare",
		message: "the zone example.com has no Workers entitlement",
	}.render(Presentation{})
	want := "WARN  [check] cloudflare: the zone example.com has no Workers entitlement"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestTheBracketNamesThePhaseInTheCommandVocabulary(t *testing.T) {
	names := map[progressv1.Phase]string{
		progressv1.Phase_PHASE_CHECK:     "INFO  [check] web: m",
		progressv1.Phase_PHASE_BUILD:     "INFO  [build] web: m",
		progressv1.Phase_PHASE_PLAN:      "INFO  [plan] web: m",
		progressv1.Phase_PHASE_PROVISION: "INFO  [provision] web: m",
		progressv1.Phase_PHASE_DEPLOY:    "INFO  [deploy] web: m",
		progressv1.Phase_PHASE_PROMOTE:   "INFO  [promote] web: m",
		progressv1.Phase_PHASE_DESTROY:   "INFO  [destroy] web: m",
	}
	for phase, want := range names {
		got := line{level: progressv1.Level_LEVEL_INFO, phase: phase, subject: "web", message: "m"}.render(Presentation{})
		if got != want {
			t.Errorf("%v: got %q, want %q", phase, got, want)
		}
	}
}

func TestTheLevelFillsAFiveCharacterColumn(t *testing.T) {
	levels := map[progressv1.Level]string{
		progressv1.Level_LEVEL_DEBUG: "DEBUG [build] web: m",
		progressv1.Level_LEVEL_INFO:  "INFO  [build] web: m",
		progressv1.Level_LEVEL_WARN:  "WARN  [build] web: m",
		progressv1.Level_LEVEL_ERROR: "ERROR [build] web: m",
	}
	for level, want := range levels {
		got := line{level: level, phase: progressv1.Phase_PHASE_BUILD, subject: "web", message: "m"}.render(Presentation{})
		if got != want {
			t.Errorf("%v: got %q, want %q", level, got, want)
		}
	}
}

func TestALineThatEndsAUnitMarksHowItEndedRightAfterThePhase(t *testing.T) {
	ok := line{
		level:   progressv1.Level_LEVEL_INFO,
		phase:   progressv1.Phase_PHASE_DEPLOY,
		subject: "web",
		message: "deployed 12 resources in 34s",
		ends:    progressv1.SpanStatus_SPAN_STATUS_OK,
	}.render(Presentation{})
	if want := "INFO  [deploy] ✓ web: deployed 12 resources in 34s"; ok != want {
		t.Errorf("got  %q\nwant %q", ok, want)
	}
	failed := line{
		level:   progressv1.Level_LEVEL_ERROR,
		phase:   progressv1.Phase_PHASE_DEPLOY,
		subject: "api",
		message: "deploy failed after 3 of 9 resources",
		ends:    progressv1.SpanStatus_SPAN_STATUS_ERROR,
	}.render(Presentation{})
	if want := "ERROR [deploy] ✗ api: deploy failed after 3 of 9 resources"; failed != want {
		t.Errorf("got  %q\nwant %q", failed, want)
	}
}

func TestALineWithNoSubjectHasNoColon(t *testing.T) {
	got := line{
		level:   progressv1.Level_LEVEL_INFO,
		phase:   progressv1.Phase_PHASE_PROMOTE,
		message: "production now serves deployment d-42",
	}.render(Presentation{})
	if want := "INFO  [promote] production now serves deployment d-42"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestALineOutsideAnyPhaseHasNoBracket(t *testing.T) {
	got := line{
		level:   progressv1.Level_LEVEL_ERROR,
		message: "interrupted before anything changed",
	}.render(Presentation{})
	if want := "ERROR interrupted before anything changed"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestAMultiLineMessageIndentsItsContinuationLinesUnderThePhase(t *testing.T) {
	got := line{
		level:   progressv1.Level_LEVEL_ERROR,
		phase:   progressv1.Phase_PHASE_BUILD,
		subject: "api",
		message: "build failed with exit status 1\nnpm ERR! missing script: build",
		ends:    progressv1.SpanStatus_SPAN_STATUS_ERROR,
	}.render(Presentation{})
	want := "ERROR [build] ✗ api: build failed with exit status 1\n" +
		"      npm ERR! missing script: build"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWithColourTheLevelAndTheUnitMarkArePaintedAndTheTextIsNot(t *testing.T) {
	colour := Presentation{Color: true}
	cases := []struct {
		name string
		line line
		want string
	}{
		{
			name: "debug is faint",
			line: line{level: progressv1.Level_LEVEL_DEBUG, phase: progressv1.Phase_PHASE_DEPLOY, message: "m"},
			want: "\x1b[2mDEBUG\x1b[22m [deploy] m",
		},
		{
			name: "info is plain",
			line: line{level: progressv1.Level_LEVEL_INFO, phase: progressv1.Phase_PHASE_DEPLOY, message: "m"},
			want: "INFO  [deploy] m",
		},
		{
			name: "a warning is bold yellow",
			line: line{level: progressv1.Level_LEVEL_WARN, phase: progressv1.Phase_PHASE_CHECK, message: "m"},
			want: "\x1b[33;1mWARN \x1b[0;22m [check] m",
		},
		{
			name: "an error is bold red",
			line: line{level: progressv1.Level_LEVEL_ERROR, phase: progressv1.Phase_PHASE_CHECK, message: "m"},
			want: "\x1b[31;1mERROR\x1b[0;22m [check] m",
		},
		{
			name: "a unit that ended well has a green mark",
			line: line{level: progressv1.Level_LEVEL_INFO, phase: progressv1.Phase_PHASE_BUILD, subject: "web", message: "m", ends: progressv1.SpanStatus_SPAN_STATUS_OK},
			want: "INFO  [build] \x1b[32m✓\x1b[0m web: m",
		},
		{
			name: "a unit that failed has a bold red mark",
			line: line{level: progressv1.Level_LEVEL_ERROR, phase: progressv1.Phase_PHASE_BUILD, subject: "api", message: "m", ends: progressv1.SpanStatus_SPAN_STATUS_ERROR},
			want: "\x1b[31;1mERROR\x1b[0;22m [build] \x1b[31;1m✗\x1b[0;22m api: m",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.line.render(colour); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}
