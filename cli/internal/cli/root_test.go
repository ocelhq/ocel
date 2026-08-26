package cli

import (
	"bytes"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/runui"
)

func TestTheRootFlagsFeedTheOneResolver(t *testing.T) {
	origFormat, origVerbose := logFormatFlag, verboseFlag
	t.Cleanup(func() { logFormatFlag, verboseFlag = origFormat, origVerbose })

	logFormatFlag, verboseFlag = string(runui.FormatJSON), true

	p := presentation(&bytes.Buffer{})
	if p.Format != runui.FormatJSON {
		t.Errorf("Format = %q, want the --log-format flag to reach the resolver", p.Format)
	}
	if !p.Verbose {
		t.Errorf("Verbose = false, want the --verbose flag to reach the resolver")
	}
}
