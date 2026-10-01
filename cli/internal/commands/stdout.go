package commands

import (
	"io"

	"github.com/spf13/cobra"
)

const stdoutAnnotation = "ocel.stdout"

const (
	stdoutData   = "data"
	stdoutReport = "report"
)

func ReserveStdout(cmd *cobra.Command) *cobra.Command {
	return reserveStdoutFor(cmd, stdoutData)
}

func ReserveStdoutForReport(cmd *cobra.Command) *cobra.Command {
	return reserveStdoutFor(cmd, stdoutReport)
}

func reserveStdoutFor(cmd *cobra.Command, use string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[stdoutAnnotation] = use
	return cmd
}

func ChooseRunOutput(cmd *cobra.Command) io.Writer {
	if isStdoutReserved(cmd) {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

func isStdoutReserved(cmd *cobra.Command) bool {
	return findStdoutUse(cmd) != ""
}

func isStdoutWrittenDuringRun(cmd *cobra.Command) bool {
	return findStdoutUse(cmd) == stdoutData
}

func findStdoutUse(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if use := c.Annotations[stdoutAnnotation]; use != "" {
			return use
		}
	}
	return ""
}
