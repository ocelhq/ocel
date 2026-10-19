package terminal

import "testing"

const consoleLink = "https://console.example.com/functions/web"

func TestAHyperlinkWrapsItsTextInOSC8WhenTheTerminalShowsColour(t *testing.T) {
	p := Resolve(Conditions{TTY: true})
	want := "\x1b]8;;" + consoleLink + "\x1b\\web\x1b]8;;\x1b\\"
	if got := p.Hyperlink("web", consoleLink); got != want {
		t.Errorf("Hyperlink() = %q, want %q", got, want)
	}
}

func TestAHyperlinkIsPlainTextUnderNoColorOrADumbTerminal(t *testing.T) {
	for name, conditions := range map[string]Conditions{
		"NO_COLOR":                     {TTY: true, ColorAsked: ColorNever},
		"a dumb terminal":              {TTY: true, Dumb: true},
		"a dumb terminal forced on":    {TTY: true, Dumb: true, ColorAsked: ColorAlways},
		"a pipe forced to show colour": {ColorAsked: ColorAlways},
	} {
		if got := Resolve(conditions).Hyperlink("web", consoleLink); got != "web" {
			t.Errorf("%s: Hyperlink() = %q, want the plain text", name, got)
		}
	}
}

func TestAHyperlinkIsPlainTextUnderGitHubActionsWithoutATerminal(t *testing.T) {
	p := Resolve(Conditions{GitHubActions: true})
	if !p.Color {
		t.Fatal("a GitHub Actions log shows no colour, want this test to cover a log that does")
	}
	if got := p.Hyperlink("web", consoleLink); got != "web" {
		t.Errorf("Hyperlink() = %q, want the plain text: the Actions log viewer does not render OSC 8", got)
	}
}

func TestAHyperlinkOfAURLCarryingAControlByteIsPlainText(t *testing.T) {
	p := Resolve(Conditions{TTY: true})
	for _, url := range []string{
		"https://console.example.com/\x1b]8;;evil",
		"https://console.example.com/\x07",
		"https://console.example.com/\x7f",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"/relative/path",
		"https://",
		"",
	} {
		if got := p.Hyperlink("web", url); got != "web" {
			t.Errorf("Hyperlink(%q) = %q, want the plain text", url, got)
		}
	}
}
