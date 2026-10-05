package commands

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

const NoBrowserEnvVar = "OCEL_NO_BROWSER"

const ConfigEnvVar = "OCEL_CONFIG"

const DebugEnvVar = "OCEL_DEBUG"

const JSONEnvVar = "OCEL_JSON"

type Invocation struct {
	Events          *run.Bus
	Presentation    func(w io.Writer) terminal.Presentation
	StdinIsTerminal func(r io.Reader) bool
	JSON            func() bool
	Questions       providerprocess.Questions
	ConfigPath      func() string
	Setups          prerequisite.Setups
}

func (i Invocation) LoadProject(ctx context.Context, cwd string) (*project.Project, error) {
	return project.Load(ctx, cwd, i.ConfigPath())
}

func (i Invocation) EnsureProject(ctx context.Context, cwd string, policy consent.Policy) (*project.Project, error) {
	var cfg *project.Project
	preamble := i.Events.Preamble(ctx)
	err := i.Setups.Ensure(ctx, policy, preamble, func(ctx context.Context) (err error) {
		cfg, err = i.LoadProject(ctx, cwd)
		return err
	})
	if prerequisite.IsDeclined(err) {
		preamble.Say(err.Error())
	}
	return cfg, err
}

func (i Invocation) LoadOptionalProject(ctx context.Context, cwd string) (*project.Project, error) {
	return project.LoadOptional(ctx, cwd, i.ConfigPath())
}

func (i Invocation) CanAsk(stdin io.Reader) bool {
	return i.StdinIsTerminal(stdin) && (i.JSON == nil || !i.JSON())
}

func (i Invocation) IsBrowserReachable(stdin io.Reader) bool {
	return os.Getenv(NoBrowserEnvVar) == "" && i.StdinIsTerminal(stdin)
}

func (i Invocation) AttachCommandSink(cmd *cobra.Command) {
	w := ChooseRunOutput(cmd)
	present := i.Presentation(w)
	present.SharedTerminal = isTerminalSharedWithChild(cmd) || isStdoutWrittenDuringRun(cmd) && terminal.IsTerminal(cmd.OutOrStdout())
	i.Events.Attach(terminal.NewSink(present, w))
}
