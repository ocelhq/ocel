package root

import (
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const bareCommandName = "help"

var commandsThatRecordNothing = []string{"telemetry flush"}

func (c *command) recordCommandCompleted(args []string, err error, exitCode int, elapsed time.Duration) {
	resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
	if !resolution.Enabled || !c.isRecorded() {
		return
	}
	c.recordEvent(func(identity telemetry.Identity, at time.Time) (telemetry.Event, error) {
		return telemetry.NewCommandCompleted(identity, at, telemetry.CommandCompletion{
			Command:   c.formatCommandPath(),
			Flags:     c.listSetFlagNames(),
			ExitCode:  exitCode,
			ErrorCode: clierror.NewRunError(err).GetCode(),
			Duration:  elapsed,
			JSON:      jsonRequested(args),
			TTY:       terminal.IsTerminal(c.root.OutOrStdout()),
		})
	})
	c.startFlush(resolution)
}

func (c *command) recordEvent(build func(identity telemetry.Identity, at time.Time) (telemetry.Event, error)) {
	resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
	if !resolution.Enabled {
		return
	}
	installID, idErr := userconfig.EnsureInstallID()
	if idErr != nil {
		return
	}
	event, buildErr := build(telemetry.NewIdentity(installID), time.Now())
	if buildErr != nil {
		return
	}
	telemetry.Submit(c.root.ErrOrStderr(), resolution, event)
}

func (c *command) isRecorded() bool {
	if c.invoked != nil && isShellCompletion(c.invoked) {
		return false
	}
	return !slices.Contains(commandsThatRecordNothing, c.formatCommandPath())
}

func (c *command) formatCommandPath() string {
	if c.invoked == nil || c.invoked == c.root {
		return bareCommandName
	}
	return strings.TrimPrefix(c.invoked.CommandPath(), c.root.Name()+" ")
}

func (c *command) listSetFlagNames() []string {
	names := []string{}
	if c.invoked == nil {
		return names
	}
	c.invoked.Flags().Visit(func(flag *pflag.Flag) { names = append(names, flag.Name) })
	slices.Sort(names)
	return names
}
