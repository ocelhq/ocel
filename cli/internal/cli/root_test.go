package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/obs"
)

func TestLogFormatIsIndependentOfVerbosity(t *testing.T) {
	origFormat, origVerbose := logFormatFlag, verboseFlag
	t.Cleanup(func() { logFormatFlag, verboseFlag = origFormat, origVerbose })

	logFormatFlag, verboseFlag = logFormatHuman, false
	if logFormat() != logFormatHuman {
		t.Fatalf("logFormat() = %q, want %q", logFormat(), logFormatHuman)
	}

	verboseFlag = true
	if got := logFormat(); got != logFormatHuman {
		t.Errorf("turning verbose on changed logFormat() to %q, the two flags must be independent axes", got)
	}
	if !verboseEnabled() {
		t.Errorf("verboseEnabled() = false with verboseFlag set")
	}

	logFormatFlag = logFormatJSON
	if !verboseEnabled() {
		t.Errorf("switching to json format turned verbosity off; the two flags must be independent axes")
	}

	verboseFlag = false
	if got := logFormat(); got != logFormatJSON {
		t.Errorf("turning verbose off changed logFormat() to %q, want it to stay %q", got, logFormatJSON)
	}
}

func TestLogFormatFlagReachesTheSessionOutput(t *testing.T) {
	origFormat := logFormatFlag
	t.Cleanup(func() { logFormatFlag = origFormat })

	dir := t.TempDir()
	_, run, err := obs.Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("obs.Start() = %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })

	logFormatFlag = logFormatJSON
	var out bytes.Buffer
	s := deployui.New(&out, run, sessionFormat(), false)
	t.Cleanup(func() { _ = s.Close() })

	s.Building()

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
		t.Fatalf("--log-format json did not reach the session: stdout = %q is not JSON: %v", out.String(), err)
	}
}

func TestLogFormatDefaultsToHumanOnAnyUnrecognisedValue(t *testing.T) {
	origFormat := logFormatFlag
	t.Cleanup(func() { logFormatFlag = origFormat })

	logFormatFlag = "yaml"
	if got := logFormat(); got != logFormatHuman {
		t.Errorf("logFormat() = %q for an unrecognised value, want it to fall back to %q", got, logFormatHuman)
	}
}
