package commands

import "github.com/spf13/cobra"

const YesUsage = "Consent in advance to any confirmation this command would ask for"

func AddYesFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVarP(into, "yes", "y", false, YesUsage)
}
