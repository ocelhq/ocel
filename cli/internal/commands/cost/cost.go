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
	"github.com/ocelhq/ocel/pkg/pricing"
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
		Short: "Estimate the monthly bill for what a deploy, a plan or a state would provision",
		Long: "Estimate the monthly bill for what a deploy, a plan or a state would provision.\n\n" +
			"Without --from, reads the project's config and declarations and asks the provider what a " +
			"deploy would create. With --from, reads a Terraform plan or state, a Pulumi preview or " +
			"stack export, an SST state export, or an earlier `ocel cost scan --json`, and needs no " +
			"project. Each resource is priced against the console's price catalog in one lookup; no " +
			"resource names leave the machine. Fixed costs accrue whether or not traffic arrives; usage " +
			"costs follow the profile, or the quantities a usage file sets per resource.\n\n" +
			"--against prices a second document as the current set and shows what changes; --diff " +
			"takes the current set from the prior state a plan or preview carries.\n\n" +
			"`pulumi preview --json` lists unchanged resources only with --show-sames, so pass it to price " +
			"the whole stack. `sst diff --json` lists changes alone, so it suits --diff only.",
		Example: "  $ ocel cost scan\n" +
			"  $ ocel cost scan --env preview --profile heavy\n" +
			"  $ ocel cost scan --usage usage.yaml\n" +
			"  $ terraform show -json plan.out | ocel cost scan --from - --diff\n" +
			"  $ pulumi preview --json --show-sames | ocel cost scan --from - --diff\n" +
			"  $ ocel cost scan --from plan.json --against state.json\n" +
			"  $ ocel cost scan --offline --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return Run(cmd.Context(), dependencies, cwd, opts, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Env, "env", "", "Environment to price: production (the default) or preview")
	cmd.Flags().StringVar(&opts.Profile, "profile", pricing.ProfileName(pricing.DefaultProfile), "Usage assumptions for the usage-based rows: "+strings.Join(profileNames(), ", "))
	cmd.Flags().StringVar(&opts.Usage, "usage", "", "YAML or JSON `file` of monthly quantities keyed by resource id, overriding the profile")
	cmd.Flags().StringVar(&opts.From, "from", "", "Plan, state or scan `file` to price instead of the project, or - for stdin")
	cmd.Flags().StringVar(&opts.Against, "against", "", "Plan, state or scan `file` holding the current set to compare with, or - for stdin")
	cmd.Flags().BoolVar(&opts.Diff, "diff", false, "Compare with the prior state the --from plan or preview carries")
	cmd.Flags().BoolVar(&opts.Offline, "offline", false, "Price from cached answers only, never asking the console")
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.CostScanResult{}, &resultv1.CostDiffResult{})
}
