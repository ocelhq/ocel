// Package cli implements the Ocel command-line interface.
package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X github.com/ocelhq/ocel/internal/cli.version=...".
var version = "dev"

// apiURLFlag is bound to the root persistent --api-url flag, shared by every
// subcommand. Empty unless explicitly set; the default target is resolved
// per command (see effectiveAPIURL / resolveAPIURL).
var apiURLFlag string

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
	rootCmd.PersistentFlags().StringVar(&apiURLFlag, "api-url", "", "Base URL of the Ocel server (defaults to $OCEL_API_URL, else https://ocel.app)")

	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}

// effectiveAPIURL resolves the Ocel API origin a command should target, in
// decreasing precedence: an explicit --api-url flag, the persisted
// credentials' APIURL (credsURL), then the resolved default (see
// resolveAPIURL). cmd may be nil (e.g. in tests), in which case the flag is
// not consulted. The result is an origin with no trailing slash; callers
// append "/api/..." themselves.
func effectiveAPIURL(cmd *cobra.Command, credsURL string) string {
	if cmd != nil && cmd.Flags().Changed("api-url") {
		return strings.TrimRight(apiURLFlag, "/")
	}
	if credsURL != "" {
		return credsURL
	}
	return resolveAPIURL()
}
