package commands

import (
	"io"

	"github.com/spf13/cobra"
)

const (
	stdoutAnnotation    = "ocel.stdout"
	runEventsAnnotation = "ocel.run-events"
)

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

func DeclareRunEvents(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[runEventsAnnotation] = "true"
	return cmd
}

func PrintsRunEvents(cmd *cobra.Command) bool {
	return cmd.Annotations[runEventsAnnotation] != ""
}

func isStdoutReserved(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[stdoutAnnotation] != "" {
			return true
		}
	}
	return false
}
