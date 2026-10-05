package telemetry_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

const (
	aKey       = "a-key"
	anEndpoint = "https://events.example"
)

func resolveWith(t *testing.T, key string, env map[string]string) telemetry.Resolution {
	t.Helper()
	for name, value := range env {
		t.Setenv(name, value)
	}
	return telemetry.Resolve(key, anEndpoint)
}

func TestTelemetryIsEnabledWhenNothingDisablesIt(t *testing.T) {
	got := resolveWith(t, aKey, nil)

	if !got.Enabled || got.Debug || got.Rule != telemetry.RuleDefault {
		t.Errorf("Resolve = %+v, want enabled, not debug, decided by the default", got)
	}
}

func TestABuildWithoutAKeyDisablesTelemetry(t *testing.T) {
	got := resolveWith(t, "", nil)

	if got.Enabled || got.Rule != telemetry.RuleNoKey {
		t.Errorf("Resolve = %+v, want disabled by the missing key", got)
	}
}

func TestOCELTelemetryOffDisablesTelemetry(t *testing.T) {
	for _, value := range []string{"0", "false", "FALSE", "False"} {
		t.Run(value, func(t *testing.T) {
			got := resolveWith(t, aKey, map[string]string{"OCEL_TELEMETRY": value})

			if got.Enabled || got.Rule != telemetry.RuleOptedOut {
				t.Errorf("Resolve = %+v, want disabled by OCEL_TELEMETRY=%s", got, value)
			}
		})
	}
}

func TestDoNotTrackDisablesTelemetry(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			got := resolveWith(t, aKey, map[string]string{"DO_NOT_TRACK": value})

			if got.Enabled || got.Rule != telemetry.RuleDoNotTrack {
				t.Errorf("Resolve = %+v, want disabled by DO_NOT_TRACK=%s", got, value)
			}
		})
	}
}

func TestDoNotTrackThatIsNotTruthyLeavesTelemetryOn(t *testing.T) {
	for _, value := range []string{"0", "false", "", "yes"} {
		t.Run(value, func(t *testing.T) {
			got := resolveWith(t, aKey, map[string]string{"DO_NOT_TRACK": value})

			if !got.Enabled {
				t.Errorf("Resolve = %+v, want DO_NOT_TRACK=%q to leave telemetry on", got, value)
			}
		})
	}
}

func TestOCELTelemetryDebugResolvesAsDebugMode(t *testing.T) {
	for _, value := range []string{"debug", "DEBUG"} {
		t.Run(value, func(t *testing.T) {
			got := resolveWith(t, aKey, map[string]string{"OCEL_TELEMETRY": value})

			if !got.Enabled || !got.Debug || got.Rule != telemetry.RuleDebug {
				t.Errorf("Resolve = %+v, want enabled debug mode decided by the debug rule", got)
			}
		})
	}
}

func TestDoNotTrackBeatsDebugMode(t *testing.T) {
	got := resolveWith(t, aKey, map[string]string{"OCEL_TELEMETRY": "debug", "DO_NOT_TRACK": "1"})

	if got.Enabled || got.Debug || got.Rule != telemetry.RuleDoNotTrack {
		t.Errorf("Resolve = %+v, want DO_NOT_TRACK to disable debug mode too", got)
	}
}

func TestOptingOutDecidesEvenWithoutAKey(t *testing.T) {
	got := resolveWith(t, "", map[string]string{"OCEL_TELEMETRY": "0"})

	if got.Enabled || got.Debug || got.Rule != telemetry.RuleOptedOut {
		t.Errorf("Resolve = %+v, want OCEL_TELEMETRY=0 to decide before the missing key", got)
	}
}

func TestDebugModeWorksWithoutAKey(t *testing.T) {
	got := resolveWith(t, "", map[string]string{"OCEL_TELEMETRY": "debug"})

	if !got.Enabled || !got.Debug || got.Rule != telemetry.RuleDebug {
		t.Errorf("Resolve = %+v, want debug mode in a keyless build, since debug prints instead of sending", got)
	}
}

func TestOptOutsBeatDebugModeWithoutAKey(t *testing.T) {
	got := resolveWith(t, "", map[string]string{"OCEL_TELEMETRY": "debug", "DO_NOT_TRACK": "1"})

	if got.Enabled || got.Debug || got.Rule != telemetry.RuleDoNotTrack {
		t.Errorf("Resolve = %+v, want DO_NOT_TRACK to win over debug mode in a keyless build", got)
	}
}

func TestABuildWithoutLinkerValuesResolvesTelemetryOffForWantOfAKey(t *testing.T) {
	got := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)

	if got.Enabled || got.Rule != telemetry.RuleNoKey {
		t.Errorf("Resolve(WriteKey, Endpoint) = %+v, want disabled by the missing key in a build with no linker values", got)
	}
}

func TestABuildWithoutAnEndpointDisablesTelemetry(t *testing.T) {
	got := telemetry.Resolve(aKey, "")

	if got.Enabled || got.Rule != telemetry.RuleNoEndpoint {
		t.Errorf("Resolve = %+v, want disabled by the missing endpoint", got)
	}
}

func TestDebugModeWorksWithoutAnEndpoint(t *testing.T) {
	t.Setenv("OCEL_TELEMETRY", "debug")

	got := telemetry.Resolve(aKey, "")

	if !got.Enabled || !got.Debug || got.Rule != telemetry.RuleDebug {
		t.Errorf("Resolve = %+v, want debug mode in a build without an endpoint, since debug prints instead of sending", got)
	}
}

func TestOnlyAnEnabledResolutionOutsideDebugModeCollects(t *testing.T) {
	cases := map[string]struct {
		resolution telemetry.Resolution
		want       bool
	}{
		"enabled":  {telemetry.Resolution{Enabled: true}, true},
		"debug":    {telemetry.Resolution{Enabled: true, Debug: true}, false},
		"disabled": {telemetry.Resolution{}, false},
	}
	for name, tc := range cases {
		if got := tc.resolution.IsCollecting(); got != tc.want {
			t.Errorf("%s: IsCollecting() = %v, want %v", name, got, tc.want)
		}
	}
}
