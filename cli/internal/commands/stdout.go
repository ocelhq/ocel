package commands

import (
	"io"

	"github.com/spf13/cobra"
)

const stdoutAnnotation = "ocel.stdout"

func ReserveStdout(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[stdoutAnnotation] = "true"
	return cmd
}

func ChooseRunOutput(cmd *cobra.Command) io.Writer {
	if isStdoutReserved(cmd) {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

func PrintsData(cmd *cobra.Command) bool {
	return isStdoutReserved(cmd) && !isTerminalSharedWithChild(cmd)
}

func DrawsRunOnStdout(cmd *cobra.Command) bool {
	return !isStdoutReserved(cmd)
}

func isStdoutReserved(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[stdoutAnnotation] != "" {
			return true
		}
	}
	return false
}
