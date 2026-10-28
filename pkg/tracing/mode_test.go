package tracing_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestATraceModeIsOnUnlessItsVariableSaysOffOrFull(t *testing.T) {
	for value, want := range map[string]tracing.Mode{"": tracing.ModeOn, "on": tracing.ModeOn, "off": tracing.ModeOff, "full": tracing.ModeFull} {
		got, err := tracing.ParseMode(value)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v, want %q", value, got, err, want)
		}
	}
}

func TestAnUnknownTraceModeIsRefusedNamingTheModesThereAre(t *testing.T) {
	for _, value := range []string{"verbose", "ON", "1"} {
		_, err := tracing.ParseMode(value)
		if err == nil {
			t.Errorf("ParseMode(%q) = nil error, want a refusal", value)
			continue
		}
		for _, named := range []string{tracing.ModeEnvVar, `"` + value + `"`, "on", "off", "full"} {
			if !strings.Contains(err.Error(), named) {
				t.Errorf("ParseMode(%q) = %q, want it to name %s", value, err, named)
			}
		}
	}
}

func TestTheTraceModeIsReadFromItsVariable(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, "full")
	if got := tracing.ReadMode(); got != tracing.ModeFull {
		t.Errorf("ReadMode() = %q under %s=full, want full", got, tracing.ModeEnvVar)
	}
	t.Setenv(tracing.ModeEnvVar, "nonsense")
	if got := tracing.ReadMode(); got != tracing.ModeOn {
		t.Errorf("ReadMode() = %q under a value no CLI accepts, want on", got)
	}
}

func TestTheTraceVariablesKeepTheNamesTheNodeHalfAndOcelLiveRead(t *testing.T) {
	t.Parallel()
	for got, want := range map[string]string{
		tracing.ModeEnvVar:           "OCEL_TRACE",
		tracing.CollectorURLEnvVar:   "OCEL_TRACE_COLLECTOR_URL",
		tracing.CollectorTokenEnvVar: "OCEL_TRACE_COLLECTOR_TOKEN",
		tracing.TraceParentEnvVar:    "TRACEPARENT",
	} {
		if got != want {
			t.Errorf("environment name = %q, want %q", got, want)
		}
	}
}
