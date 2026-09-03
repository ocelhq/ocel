package runui

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func TestOwedVariablesArePaintedOnlyWhenColourIsOn(t *testing.T) {
	t.Parallel()
	ev := &streamv1.RunEvent{Event: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{
		Url: "http://127.0.0.1:5555/#t=abc",
		Owed: &streamv1.VariablesOwed{Cells: []*streamv1.OwedVariable{
			{Key: "DATABASE_URL", Reason: "no value"},
			{Key: "PORT", Folder: "/web", Reason: "set, but not a number"},
		}},
	}}}

	painted := strings.Join(newProjector(Presentation{Format: FormatHuman, Color: true, Width: defaultWidth}).project(ev), "\n")
	plain := strings.Join(newProjector(Presentation{Format: FormatHuman, Width: defaultWidth}).project(ev), "\n")

	for _, want := range []string{"\x1b[31m✗\x1b[0m DATABASE_URL", "\x1b[31m✗\x1b[0m PORT", "\x1b[2m/web\x1b[22m"} {
		if !strings.Contains(painted, want) {
			t.Errorf("painted = %q, want it to contain %q", painted, want)
		}
	}
	if strings.Contains(painted, "\x1b[31mno value") || strings.Contains(painted, "\x1b[2mDATABASE_URL") {
		t.Errorf("painted = %q, want the reason and the key left unpainted", painted)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("plain = %q, want no escape codes without colour", plain)
	}
	if stripped := stripANSI(painted); stripped != plain {
		t.Errorf("painted minus codes =\n%s\nwant the plain form\n%s", stripped, plain)
	}
}

func TestARefusalIsTheFailureNotADetailUnderOne(t *testing.T) {
	t.Parallel()
	s, out, _ := newTestSession(t, "ocel deploy")
	refusal := &envgate.Refusal{Problems: []*resourcesv1.VariableProblem{
		{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
	}}
	s.Fail(errors.Join(refusal, errors.New("the variables UI closed before the matrix was complete.")))

	got := out.String()
	for _, want := range []string{
		"✗ 1 variable is not ready — nothing has been built.\n\n  ✗ STRIPE_API_KEY  root  no value\n\n  Fill them in: ocel env set STRIPE_API_KEY <VALUE>\n\n  the variables UI closed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "✗ Failed") {
		t.Errorf("stdout = %q, want the refusal to stand as the failure headline", got)
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
