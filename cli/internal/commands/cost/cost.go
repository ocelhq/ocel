package cost

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

type Dependencies struct {
	commands.Invocation
	ReadFunctions       func(projectDir string) ([]build.Function, error)
	CollectDeclarations func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost <command>",
		Short: "Estimate what this project costs to run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newScanCommand(dependencies))
	return commands.DeclareReadOnly(commands.ReserveStdout(cmd))
}

func newScanCommand(dependencies Dependencies) *cobra.Command {
	var opts Options
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Estimate the monthly bill for what a deploy would provision",
		Long: "Estimate the monthly bill for what a deploy would provision.\n\n" +
			"Reads the project's config and declarations, asks the provider what a deploy would " +
			"create and what each piece costs, and prints the estimate. Nothing is built and no " +
			"credentials are read. Fixed costs accrue whether or not traffic arrives; usage costs " +
			"follow the profile, or the quantities a usage file sets per resource.",
		Example: "  $ ocel cost scan\n" +
			"  $ ocel cost scan --env preview --profile heavy\n" +
			"  $ ocel cost scan --usage usage.yaml\n" +
			"  $ ocel cost scan --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return Run(cmd.Context(), dependencies, cwd, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Env, "env", envProduction, "Environment to price: production or preview")
	cmd.Flags().StringVar(&opts.Profile, "profile", profileName(defaultProfile), "Usage assumptions for the usage-based rows: "+strings.Join(profileNames(), ", "))
	cmd.Flags().StringVar(&opts.Usage, "usage", "", "YAML or JSON `file` of monthly quantities keyed by resource id, overriding the profile")
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.CostScanResult{})
}
