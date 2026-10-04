package commands

import "github.com/spf13/cobra"

const YesUsage = "Consent in advance to any confirmation this command would ask for"

const (
	yesFlag = "yes"
	dryFlag = "dry"
)

func AddYesFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVarP(into, yesFlag, "y", false, YesUsage)
}

func AddDryFlag(cmd *cobra.Command, into *bool, usage string) {
	cmd.Flags().BoolVar(into, dryFlag, false, usage)
}

func HasConfirmationFlag(cmd *cobra.Command) bool {
	return cmd.Flags().Lookup(yesFlag) != nil || cmd.Flags().Lookup(dryFlag) != nil
}
