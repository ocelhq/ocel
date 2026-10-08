package root

import (
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

const bareCommandName = "help"

var commandsThatRecordNothing = []string{"telemetry flush"}

func (c *command) recordCommandCompleted(args []string, err error, exitCode int, elapsed time.Duration) {
	resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
	if !resolution.Enabled || !c.isRecorded() {
		return
	}
	c.recordEvent(telemetry.CommandCompletion{
		Command:        c.formatCommandPath(),
		Flags:          c.listSetFlagNames(),
		ExitCode:       exitCode,
		ErrorCode:      clierror.NewRunError(err).GetCode(),
		Duration:       elapsed,
		JSON:           jsonRequested(args),
		TTY:            terminal.IsTerminal(c.root.OutOrStdout()),
		SkillInstalled: c.isSkillInstalled(),
	})
	c.startFlush(resolution)
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
