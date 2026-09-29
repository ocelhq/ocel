package terminal

import (
	"bytes"
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func drawn(ev *streamv1.RunEvent, present Presentation) string {
	var out bytes.Buffer
	newTranscript(&out, present, nil).Receive(ev)
	return out.String()
}

func TestMissingVariablesArePaintedOnlyWhenColourIsOn(t *testing.T) {
	t.Parallel()
	ev := &streamv1.RunEvent{Cli: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{
		Url: "http://127.0.0.1:5555/#t=abc",
		Missing: &streamv1.MissingVariables{Cells: []*streamv1.MissingVariable{
			{Key: "DATABASE_URL", Reason: "no value", Description: "The primary database connection string"},
			{Key: "PORT", Folder: "/web", Reason: "set, but not a number"},
		}},
	}}}

	painted := drawn(ev, Presentation{Color: true, Width: defaultColumns})
	plain := drawn(ev, Presentation{Width: defaultColumns})

	for _, want := range []string{"\x1b[31m✗\x1b[0m DATABASE_URL", "\x1b[31m✗\x1b[0m PORT", "\x1b[90m/web\x1b[0m"} {
		if !strings.Contains(painted, want) {
			t.Errorf("painted = %q, want it to contain %q", painted, want)
		}
	}
	if strings.Contains(painted, "\x1b[31mno value") || strings.Contains(painted, "\x1b[90mDATABASE_URL") {
		t.Errorf("painted = %q, want the reason and the key left unpainted", painted)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("plain = %q, want no escape codes without colour", plain)
	}
	if !strings.Contains(plain, "The primary database connection string") {
		t.Errorf("plain = %q, want the variable description", plain)
	}
	if stripped := stripANSI(painted); stripped != plain {
		t.Errorf("painted minus codes =\n%s\nwant the plain form\n%s", stripped, plain)
	}
}

func TestTheDeployTUIHeadsAGroupOnce(t *testing.T) {
	t.Parallel()
	ev := &streamv1.RunEvent{Cli: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{
		Url: "http://127.0.0.1:5555/#t=abc",
		Missing: &streamv1.MissingVariables{
			Cells: []*streamv1.MissingVariable{
				{Key: "GITHUB_CLIENT_ID", Reason: "no value", Group: "github"},
				{Key: "DATABASE_URL", Reason: "no value"},
				{Key: "GITHUB_CLIENT_SECRET", Reason: "no value", Group: "github"},
			},
			Groups: []*streamv1.MissingGroup{{Key: "github", Description: "Sign in with GitHub"}},
		},
	}}}

	got := drawn(ev, Presentation{Width: defaultColumns})
	want := strings.Join([]string{
		"  github — set together (Sign in with GitHub)",
		"    ✗ GITHUB_CLIENT_ID      root  no value",
		"    ✗ GITHUB_CLIENT_SECRET  root  no value",
		"  ✗ DATABASE_URL            root  no value",
	}, "\n")
	if !strings.Contains(got, want) {
		t.Errorf("waiting =\n%s\nwant it to contain\n%s", got, want)
	}
	if n := strings.Count(got, "set together"); n != 1 {
		t.Errorf("waiting = %q, states %q %d times, want once for the group", got, "set together", n)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestColourTouchesOnlyTheMarkAndTheFolderOfAMissingVariable(t *testing.T) {
	t.Parallel()
	missing := &streamv1.MissingVariables{Cells: []*streamv1.MissingVariable{
		{Key: "DATABASE_URL", Reason: "no value"},
		{Key: "PORT", Folder: "/web", Reason: "set, but not a number"},
	}}
	tagged := strings.NewReplacer("\x1b[31m", "<red>", "\x1b[90m", "<dim>", "\x1b[0m", "</>")
	got := tagged.Replace(strings.Join(MissingVariablesLines(missing, Presentation{Color: true}), "\n"))
	want := strings.Join([]string{
		"<red>✗</> 2 variables are not ready — nothing has been built.",
		"",
		"  <red>✗</> DATABASE_URL  <dim>root</>  no value",
		"  <red>✗</> PORT          <dim>/web</>  set, but not a number",
	}, "\n")
	if got != want {
		t.Errorf("MissingVariablesLines() =\n%s\nwant\n%s", got, want)
	}
	stripped := strings.NewReplacer("<red>", "", "<dim>", "", "</>", "").Replace(got)
	if plain := strings.Join(MissingVariablesLines(missing, Presentation{}), "\n"); stripped != plain {
		t.Errorf("coloured minus codes =\n%s\nwant the plain form\n%s", stripped, plain)
	}
}
