package runui

import (
	"bytes"
	"testing"
)

func TestTheFormatIsHumanUnlessJSONIsAskedForByName(t *testing.T) {
	for _, tc := range []struct {
		asked Format
		want  Format
	}{
		{"json", FormatJSON},
		{"human", FormatHuman},
		{"", FormatHuman},
		{"yaml", FormatHuman},
	} {
		if got := Resolve(Origin{LogFormat: tc.asked}).Format; got != tc.want {
			t.Errorf("Resolve(--log-format %q).Format = %q, want %q", tc.asked, got, tc.want)
		}
	}
}

func TestTheLiveViewNeedsHumanFormatOnATerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin Origin
		want   bool
	}{
		{"human on a terminal", Origin{LogFormat: "human", TTY: true, Width: 80, WidthMeasured: true}, true},
		{"human on a terminal that reports no width", Origin{LogFormat: "human", TTY: true, Width: 80}, false},
		{"human off a terminal", Origin{LogFormat: "human"}, false},
		{"json on a terminal", Origin{LogFormat: "json", TTY: true}, false},
		{"verbose on a terminal", Origin{LogFormat: "human", TTY: true, Verbose: true}, false},
	} {
		if got := Resolve(tc.origin).Live(); got != tc.want {
			t.Errorf("%s: Live() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestVerboseIsAFilterAndNotAFormat(t *testing.T) {
	quiet := Resolve(Origin{LogFormat: "json", TTY: true})
	loud := Resolve(Origin{LogFormat: "json", TTY: true, Verbose: true})

	if loud.Format != quiet.Format {
		t.Errorf("--verbose moved the format to %q, want it left at %q", loud.Format, quiet.Format)
	}
	if loud.Color != quiet.Color {
		t.Errorf("--verbose moved colour to %v, want it left at %v", loud.Color, quiet.Color)
	}
	if !loud.Verbose {
		t.Error("--verbose did not survive resolution")
	}
}

func TestColourNeedsATerminalOrAGitHubActionsLogUnlessTheEnvironmentAskedOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin Origin
		want   bool
	}{
		{"a terminal", Origin{TTY: true}, true},
		{"a dumb terminal", Origin{TTY: true, Dumb: true}, false},
		{"a pipe", Origin{}, false},
		{"a GitHub Actions log", Origin{GitHubActions: true}, true},
		{"a terminal asked for no colour", Origin{TTY: true, ColorAsked: ColorNever}, false},
		{"a GitHub Actions log asked for no colour", Origin{GitHubActions: true, ColorAsked: ColorNever}, false},
		{"a pipe asked for colour", Origin{ColorAsked: ColorAlways}, true},
		{"a dumb terminal asked for colour", Origin{TTY: true, Dumb: true, ColorAsked: ColorAlways}, true},
	} {
		if got := Resolve(tc.origin).Color; got != tc.want {
			t.Errorf("%s: Color = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDetectReadsWhetherTheEnvironmentAskedForColour(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want ColorChoice
	}{
		{"nothing set", nil, ColorAuto},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1"}, ColorNever},
		{"FORCE_COLOR", map[string]string{"FORCE_COLOR": "1"}, ColorAlways},
		{"FORCE_COLOR at a colour depth", map[string]string{"FORCE_COLOR": "3"}, ColorAlways},
		{"FORCE_COLOR=0", map[string]string{"FORCE_COLOR": "0"}, ColorNever},
		{"FORCE_COLOR=false", map[string]string{"FORCE_COLOR": "false"}, ColorNever},
		{"CLICOLOR_FORCE", map[string]string{"CLICOLOR_FORCE": "1"}, ColorAlways},
		{"CLICOLOR_FORCE=0", map[string]string{"CLICOLOR_FORCE": "0"}, ColorAuto},
		{"NO_COLOR over FORCE_COLOR", map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, ColorNever},
		{"FORCE_COLOR=0 over CLICOLOR_FORCE", map[string]string{"FORCE_COLOR": "0", "CLICOLOR_FORCE": "1"}, ColorNever},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE"} {
				t.Setenv(name, tc.env[name])
			}
			if got := colorAsked(); got != tc.want {
				t.Errorf("colorAsked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestForcedColourReachesAWriterThatIsNoTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("GITHUB_ACTIONS", "")

	if !IsColored(&bytes.Buffer{}) {
		t.Error("IsColored() = false with FORCE_COLOR set, want true")
	}
}

func TestOnlyTheTerminalFactsMoveWhenTheTerminalGoesAway(t *testing.T) {
	terminal := Resolve(Origin{LogFormat: "json", Verbose: true, TTY: true, Width: 120})
	pipe := Resolve(Origin{LogFormat: "json", Verbose: true, Width: 120})

	if terminal.Format != pipe.Format || terminal.Verbose != pipe.Verbose || terminal.Width != pipe.Width {
		t.Errorf("off a terminal the flags resolved to %+v, want the same as %+v", pipe, terminal)
	}
}

func TestDetectReadsTheTerminalTheCommandWasGiven(t *testing.T) {
	t.Setenv("COLUMNS", "40")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("GITHUB_ACTIONS", "")

	p := Detect("json", true, &bytes.Buffer{})

	if p.Format != FormatJSON || !p.Verbose {
		t.Errorf("Detect() = %+v, want the flags it was handed", p)
	}
	if p.TTY || p.Color {
		t.Errorf("Detect() = %+v, want a buffer treated as no terminal", p)
	}
	if p.Width != 40 {
		t.Errorf("Width = %d, want the 40 columns $COLUMNS declares", p.Width)
	}
}

func TestAnUnknownWidthFallsBackToEightyColumns(t *testing.T) {
	if got := Resolve(Origin{TTY: true}).Width; got != defaultWidth {
		t.Errorf("Width = %d, want %d when the terminal reports none", got, defaultWidth)
	}
	if got := Resolve(Origin{TTY: true, Width: 120}).Width; got != 120 {
		t.Errorf("Width = %d, want the 120 the terminal reported", got)
	}
}

func TestAGitHubActionsRunnerIsCarriedIntoThePresentation(t *testing.T) {
	t.Parallel()

	if !Resolve(Origin{LogFormat: "human", GitHubActions: true}).GitHubActions {
		t.Error("Resolve() dropped the GitHub Actions runner the origin declared")
	}
	if Resolve(Origin{LogFormat: "human"}).GitHubActions {
		t.Error("Resolve() declared a GitHub Actions runner the origin did not")
	}
}

func TestDetectRecognisesAGitHubActionsRunnerOnlyWhenItsVariableIsTrue(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "": false, "false": false, "1": false} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", value)
			if got := Detect("human", false, &bytes.Buffer{}).GitHubActions; got != want {
				t.Errorf("GITHUB_ACTIONS=%q: GitHubActions = %v, want %v", value, got, want)
			}
		})
	}
}
