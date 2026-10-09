package commands

import "github.com/spf13/cobra"

const YesUsage = "Consent in advance to any confirmation this command would ask for"

const (
	yesFlag          = "yes"
	dryFlag          = "dry"
	writesAnnotation = "ocel.yes-only-writes"
)

func AddYesFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVarP(into, yesFlag, "y", false, YesUsage)
}

func AddWriteYesFlag(cmd *cobra.Command, into *bool, usage string) {
	cmd.Flags().BoolVarP(into, yesFlag, "y", false, usage)
	if err := cmd.Flags().SetAnnotation(yesFlag, writesAnnotation, []string{"true"}); err != nil {
		panic(err)
	}
}

func AddDryFlag(cmd *cobra.Command, into *bool, usage string) {
	cmd.Flags().BoolVar(into, dryFlag, false, usage)
}

func HasConfirmationFlag(cmd *cobra.Command) bool {
	if cmd.Flags().Lookup(dryFlag) != nil {
		return true
	}
	yes := cmd.Flags().Lookup(yesFlag)
	return yes != nil && yes.Annotations[writesAnnotation] == nil
}
