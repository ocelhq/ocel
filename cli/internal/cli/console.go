package cli

import (
	"github.com/spf13/cobra"
)

var consoleCmd = &cobra.Command{
	Use:   "console",
	Short: "Manage this directory's binding to an Ocel console project",
	Long: "Manage this directory's binding to an Ocel console project.\n\n" +
		"The binding lives in .ocel/console.json, which is untracked — a clone can be " +
		"bound to a different account or project, or to none at all.",
	Args: cobra.NoArgs,
}

func init() {
	consoleLinkCmd.Flags().StringVar(&consoleLinkOpts.org, "org", "", "Select an organization by slug, bypassing the interactive picker")
	consoleLinkCmd.Flags().BoolVar(&consoleLinkOpts.create, "create", false, "Create a new project instead of selecting an existing one")

	consoleCmd.AddCommand(consoleLinkCmd)
	consoleCmd.AddCommand(consoleUnlinkCmd)
	rootCmd.AddCommand(consoleCmd)
}
