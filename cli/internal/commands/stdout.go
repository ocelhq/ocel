package commands

import (
	"io"

	"github.com/spf13/cobra"
)

const (
	stdoutAnnotation    = "ocel.stdout"
	jsonOnlyAnnotation  = "ocel.stdout-json-only"
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

func PrintOnlyUnderJSON(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[jsonOnlyAnnotation] = "true"
	return cmd
}

func PrintsTextData(cmd *cobra.Command) bool {
	return PrintsData(cmd) && cmd.Annotations[jsonOnlyAnnotation] == ""
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
		switch {
		case c.Annotations[stdoutAnnotation] != "":
			return true
		case c.Annotations[runEventsAnnotation] != "":
			return false
		}
	}
	return false
}
