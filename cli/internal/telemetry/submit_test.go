package telemetry_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func TestSubmitSpoolsTheEventInTheUserCacheOnlyWhenEnabledOutsideDebugMode(t *testing.T) {
	cases := map[string]struct {
		resolution  telemetry.Resolution
		wantSpooled bool
	}{
		"enabled":  {telemetry.Resolution{Enabled: true}, true},
		"debug":    {telemetry.Resolution{Enabled: true, Debug: true}, false},
		"disabled": {telemetry.Resolution{}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			confighome.Isolate(t)
			telemetry.PrintBannerOnce(io.Discard, telemetry.Resolution{Enabled: true})

			telemetry.Submit(io.Discard, tc.resolution, aCompletedEvent(t, "deploy"))

			spool, err := telemetry.OpenSpool()
			if err != nil {
				t.Fatal(err)
			}
			if got := spool.HasEvents(); got != tc.wantSpooled {
				t.Errorf("spool has events = %v, want %v", got, tc.wantSpooled)
			}
			if tc.wantSpooled {
				cache, _ := os.UserCacheDir()
				if _, err := os.Stat(filepath.Join(cache, "ocel", "telemetry", "events.jsonl")); err != nil {
					t.Errorf("spool file is not under the user cache's ocel directory: %v", err)
				}
			}
		})
	}
}

func TestSubmitSpoolsNothingBeforeTheFirstRunBannerIsShown(t *testing.T) {
	confighome.Isolate(t)

	telemetry.Submit(io.Discard, telemetry.Resolution{Enabled: true}, aCompletedEvent(t, "deploy"))

	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	if spool.HasEvents() {
		t.Error("the spool holds an event recorded before the banner told the user about telemetry")
	}
}
