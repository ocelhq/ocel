package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestASurveyLineCutShortIsRefusedRatherThanRead(t *testing.T) {
	t.Parallel()

	for what, line := range map[string]string{
		"a probe that could not look": kindUnreadable,
		"a path that pointed away":    kindLink,
		"a probe naming only a kind":  kindUnreadable + "\t",
	} {
		_, _, err := readSurvey(line + "\n")
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("readSurvey over %s = %v, want a refusal rather than a host read as carrying nothing", what, err)
			continue
		}
		if !strings.Contains(refusal.Message, "again") {
			t.Errorf("readSurvey over %s refused with %q, want it to say what to do about it", what, refusal.Message)
		}
	}
}

func TestTheEngineProbeSaysSystemdCouldNotBeAskedAndWhatToDoAboutIt(t *testing.T) {
	t.Parallel()

	probe := engineProbe()
	for _, want := range []string{"systemd", "systemctl status"} {
		if !strings.Contains(probe, want) {
			t.Errorf("the engine probe refuses without naming %q, so an operator on a host systemd does not serve is told only that something stopped a probe:\n%s", want, probe)
		}
	}
}

func TestAProbeThatCouldNotLookNamesWhatItCouldNotRead(t *testing.T) {
	t.Parallel()

	_, _, err := readSurvey(strings.Join([]string{kindUnreadable, "docker", "0", KindEngine, "systemctl would not run"}, "\t") + "\n")
	if err == nil {
		t.Fatal("readSurvey over a probe that could not look = nil, want a refusal")
	}
	for _, want := range []string{"docker", KindEngine, "systemctl would not run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %v, want it to carry %q", err, want)
		}
	}
}
