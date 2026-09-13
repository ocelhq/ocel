package cost

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
)

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost <command>",
		Short: "Estimate what this project costs to run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newScanCommand(deps))
	return cmd
}

func newScanCommand(deps cmddeps.Deps) *cobra.Command {
	var opts Options
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Estimate the monthly bill for what a deploy would stand up",
		Long: "Estimate the monthly bill for what a deploy would stand up.\n\n" +
			"Reads the project's config and declarations, asks the provider what a deploy would " +
			"create and what each piece costs, and prints the estimate. Nothing is built and no " +
			"credentials are read. Fixed costs stand whether or not traffic arrives; usage costs " +
			"follow the profile, or the quantities a usage file sets per resource.",
		Example: "  $ ocel cost scan\n" +
			"  $ ocel cost scan --env preview --profile heavy\n" +
			"  $ ocel cost scan --usage usage.yaml\n" +
			"  $ ocel cost scan --log-format json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return Run(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.Env, "env", envProduction, "Environment to price: production or preview")
	cmd.Flags().StringVar(&opts.Profile, "profile", profileName(defaultProfile), "Usage assumptions for the usage-based rows: "+strings.Join(profileNames(), ", "))
	cmd.Flags().StringVar(&opts.Usage, "usage", "", "YAML or JSON `file` of monthly quantities keyed by resource id, overriding the profile")
	return cmd
}
