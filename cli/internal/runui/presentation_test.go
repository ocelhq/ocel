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
		{"human on a terminal", Origin{LogFormat: "human", TTY: true}, true},
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

func TestColourNeedsATerminalThatHasNotOptedOut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin Origin
		want   bool
	}{
		{"a terminal", Origin{TTY: true}, true},
		{"a terminal with NO_COLOR set", Origin{TTY: true, NoColor: true}, false},
		{"a pipe", Origin{}, false},
	} {
		if got := Resolve(tc.origin).Color; got != tc.want {
			t.Errorf("%s: Color = %v, want %v", tc.name, got, tc.want)
		}
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
