// Package cli implements the Ocel command-line interface.
package cli

import (
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X github.com/ocelhq/ocel/internal/cli.version=...".
var version = "dev"

// rootCmd is the base command for the Ocel CLI.
var rootCmd = &cobra.Command{
	Use:           "ocel",
	Short:         "Ocel CLI",
	Long:          "Ocel CLI\n\nThe power of AWS with the DX of Vercel.",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
