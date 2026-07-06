package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// initCmd scaffolds a new Ocel project.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a new Ocel project",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), "ocel init: not implemented yet")
		return nil
	},
}
