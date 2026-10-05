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
		resolution   telemetry.Resolution
		wantSpooled  bool
		wantRecorded bool
	}{
		"enabled":  {telemetry.Resolution{Enabled: true}, true, true},
		"debug":    {telemetry.Resolution{Enabled: true, Debug: true}, false, true},
		"disabled": {telemetry.Resolution{}, false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			confighome.Isolate(t)
			telemetry.PrintBannerOnce(io.Discard, telemetry.Resolution{Enabled: true})

			recorded := telemetry.Submit(io.Discard, tc.resolution, aCompletedEvent(t, "deploy"))

			if recorded != tc.wantRecorded {
				t.Errorf("recorded = %v, want %v", recorded, tc.wantRecorded)
			}
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

	recorded := telemetry.Submit(io.Discard, telemetry.Resolution{Enabled: true}, aCompletedEvent(t, "deploy"))

	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	if recorded || spool.HasEvents() {
		t.Errorf("recorded = %v, spool has events = %v, want neither before the banner told the user about telemetry", recorded, spool.HasEvents())
	}
}

func TestSubmitReportsNothingRecordedWhenTheSpoolCannotBeWritten(t *testing.T) {
	confighome.Isolate(t)
	resolution := telemetry.Resolution{Enabled: true}
	telemetry.PrintBannerOnce(io.Discard, resolution)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "ocel"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if telemetry.Submit(io.Discard, resolution, aCompletedEvent(t, "deploy")) {
		t.Error("recorded = true, want false when the spool directory cannot be created")
	}
}
