package providerui

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

type terminalAsker struct{}

func (terminalAsker) Interactive() bool { return true }

func (terminalAsker) Confirm(context.Context, string) (bool, error) { return true, nil }

func TestTheTrustIsTheProcessTerminalNotTheProvidersLogStream(t *testing.T) {
	_, run, err := runtrace.Start(context.Background(), t.TempDir(), "ocel bootstrap production")
	if err != nil {
		t.Fatalf("runtrace.Start() = %v", err)
	}
	t.Cleanup(func() { run.Close() })

	var terminal bytes.Buffer
	deps := cmddeps.Deps{
		HostTrust: provider.Trust{Ask: terminalAsker{}, Out: &terminal},
		Verbose:   func() bool { return false },
		Format:    func() deployui.Format { return deployui.FormatHuman },
	}
	ui := deployui.New(io.Discard, run, deps.Format(), deps.Verbose())
	t.Cleanup(func() { ui.Close() })

	trust := trustFor(deps, ui)
	if trust.Ask != deps.HostTrust.Ask {
		t.Errorf("the trust asks through %#v, want the terminal the process was started on", trust.Ask)
	}
	if trust.Out != deps.HostTrust.Out {
		t.Errorf("the trust offers on %#v, want the terminal, not the provider's log stream", trust.Out)
	}
	if trust.Out == ui.BuildWriter() {
		t.Error("the trust offers on the provider's log stream, where nobody would read it")
	}
	if trust.Suspend == nil {
		t.Error("the trust has no way to stand the live view down while it asks")
	}
}
