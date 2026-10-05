package root

import (
	"errors"
	"io"
	"time"

	"github.com/spf13/pflag"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func (c *command) executeAndReport(args []string) int {
	started := time.Now()
	c.root.SetArgs(args)
	err := c.execute()
	elapsed := time.Since(started)
	exitCode := c.reportFailure(args, err)
	c.recordCommandCompleted(args, err, exitCode, elapsed)
	return exitCode
}

func (c *command) reportFailure(args []string, err error) int {
	if err == nil {
		return 0
	}
	if code, ok := exitcode.Of(err); ok {
		if jsonRequested(args) && c.invoked != nil && commands.PrintsData(c.invoked) && hasUnreportedCause(err) {
			terminal.PrintFailureJSON(c.root.OutOrStdout(), clierror.NewRunError(err))
		}
		return code
	}
	if jsonRequested(args) {
		terminal.PrintFailureJSON(c.root.OutOrStdout(), clierror.NewRunError(err))
	} else {
		terminal.PrintFailure(c.root.ErrOrStderr(), err)
	}
	return 1
}

func hasUnreportedCause(err error) bool {
	var exit *exitcode.ExitError
	for errors.As(err, &exit) {
		err = exit.Err
	}
	return err != nil
}

func jsonRequested(args []string) bool {
	var value unparsedBool
	scan := pflag.NewFlagSet("json", pflag.ContinueOnError)
	scan.ParseErrorsAllowlist.UnknownFlags = true
	scan.SetOutput(io.Discard)
	scan.Var(&value, "json", "")
	scan.Lookup("json").NoOptDefVal = "true"
	_ = scan.Parse(args)
	on, err := readFlagOrEnvBool(scan, "json", commands.JSONEnvVar)
	return on || err != nil
}

type unparsedBool string

func (b *unparsedBool) String() string { return string(*b) }

func (b *unparsedBool) Set(value string) error {
	*b = unparsedBool(value)
	return nil
}

func (*unparsedBool) Type() string { return "bool" }
