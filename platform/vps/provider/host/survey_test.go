package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestASurveyLineCutShortIsRefusedRatherThanRead(t *testing.T) {
	t.Parallel()

	for what, probe := range map[string]struct{ line, said string }{
		"a probe that could not look": {kindUnreadable, "could not check"},
		"a path that pointed away":    {kindLink, "Put a real directory or file"},
		"a probe naming only a kind":  {kindUnreadable + "\t", "could not check"},
	} {
		_, _, err := readSurvey(probe.line + "\n")
		var refusal refusal.Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("readSurvey over %s = %v, want a refusal rather than a host read as having nothing", what, err)
			continue
		}
		if !strings.Contains(refusal.Message, probe.said) {
			t.Errorf("readSurvey over %s refused with %q, want it to say %q", what, refusal.Message, probe.said)
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
			t.Errorf("refusal = %v, want it to include %q", err, want)
		}
	}
}
