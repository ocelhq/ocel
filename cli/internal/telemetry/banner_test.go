package telemetry_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/docsurl"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

var enabled = telemetry.Resolution{Enabled: true, Rule: telemetry.RuleDefault}

func announce(res telemetry.Resolution) string {
	var out bytes.Buffer
	telemetry.AnnounceOnce(&out, res)
	return out.String()
}

func TestTheBannerNamesTheDataTheDocsAndHowToTurnItOff(t *testing.T) {
	confighome.Isolate(t)

	banner := announce(enabled)

	for _, want := range []string{"anonymous usage data", docsurl.Origin + "/docs/telemetry", "OCEL_TELEMETRY=0"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner = %q, want it to contain %q", banner, want)
		}
	}
	if strings.Contains(banner, "DO_NOT_TRACK") {
		t.Errorf("banner = %q, want it never to mention DO_NOT_TRACK", banner)
	}
}

func TestTheBannerPrintsOncePerConfigHome(t *testing.T) {
	confighome.Isolate(t)

	first, second := announce(enabled), announce(enabled)

	if first == "" || second != "" {
		t.Errorf("first = %q, second = %q, want the banner once and then nothing", first, second)
	}
}

func TestTheBannerPrintsAgainInAnotherConfigHome(t *testing.T) {
	confighome.Isolate(t)
	announce(enabled)
	confighome.Isolate(t)

	if announce(enabled) == "" {
		t.Error("banner absent in a fresh config home, want it shown there")
	}
}

func TestNoBannerWhenTelemetryIsDisabled(t *testing.T) {
	confighome.Isolate(t)

	if got := announce(telemetry.Resolution{Rule: telemetry.RuleOptedOut}); got != "" {
		t.Errorf("banner = %q, want nothing while disabled", got)
	}
	if announce(enabled) == "" {
		t.Error("banner absent after a disabled run, want a disabled run not to record it as shown")
	}
}

func TestNoBannerInDebugMode(t *testing.T) {
	confighome.Isolate(t)
	debug := telemetry.Resolution{Enabled: true, Debug: true, Rule: telemetry.RuleDebug}

	if got := announce(debug); got != "" {
		t.Errorf("banner = %q, want nothing in debug mode", got)
	}
	if announce(enabled) == "" {
		t.Error("banner absent after a debug run, want a debug run not to record it as shown")
	}
}

func TestTheBannerStillPrintsWhenTheSettingsFileCannotBeWritten(t *testing.T) {
	dir := confighome.Isolate(t)
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if announce(enabled) == "" {
		t.Error("banner absent, want it shown even when the shown flag cannot be recorded")
	}
}
