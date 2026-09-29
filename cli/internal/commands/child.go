package commands

import (
	"github.com/spf13/cobra"
)

const childAnnotation = "ocel.child"

func ShareTerminalWithChild(cmd *cobra.Command) *cobra.Command {
	ReserveStdout(cmd)
	cmd.Annotations[childAnnotation] = "terminal"
	return cmd
}

func isTerminalSharedWithChild(cmd *cobra.Command) bool {
	return cmd.Annotations[childAnnotation] == "terminal"
}
