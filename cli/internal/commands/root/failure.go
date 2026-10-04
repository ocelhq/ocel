package root

import (
	"io"

	"github.com/spf13/pflag"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

const usageCode = "usage"

func (c *command) executeAndReport(args []string) (exitCode int) {
	c.root.SetArgs(args)
	err := c.execute()
	if err == nil {
		return 0
	}
	if code, ok := exitcode.Of(err); ok {
		return code
	}
	if jsonRequested(args) {
		terminal.PrintFailureJSON(c.root.OutOrStdout(), clierror.NewRunError(err))
	} else {
		terminal.PrintFailure(c.root.ErrOrStderr(), err)
	}
	return 1
}

func jsonRequested(args []string) bool {
	var on bool
	scan := pflag.NewFlagSet("json", pflag.ContinueOnError)
	scan.ParseErrorsAllowlist.UnknownFlags = true
	scan.SetOutput(io.Discard)
	scan.BoolVar(&on, "json", false, "")
	_ = scan.Parse(args)
	if scan.Changed("json") {
		return on
	}
	on, err := envBool(commands.JSONEnvVar)
	return err == nil && on
}
