package deploy

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/deploy"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const (
	prebuiltFlagUsage = "Deploy the existing " + buildoutput.StateDir + "/output instead of building first (produce it with ocel build)"
	dryFlagUsage      = "Build, print every change this would make, and stop"
)

func NewCommand(dependencies deploy.Dependencies) *cobra.Command {
	var opts deploy.Options
	var tag string

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy this project to your own infrastructure",
		Long: "Deploy this project to your own infrastructure.\n\n" +
			"Provisions the declared resources, builds the apps and releases them into your provider " +
			"account. Every deploy is kept: `ocel promotions ls` lists them, `ocel rollback` returns to one.\n\n" +
			"--dry builds, prints every change and stops.",
		Example: "  $ ocel deploy\n" +
			"  $ ocel deploy --tag v1.2.0\n" +
			"  $ ocel deploy --prebuilt\n" +
			"  $ ocel deploy --dry\n" +
			"  $ ocel deploy --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return deploy.To(cmd.Context(), dependencies, cwd, deploy.Production(tag), opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	commands.AddYesFlag(cmd, &opts.Yes)
	cmd.Flags().StringVar(&tag, "tag", "", "Mark this deploy with an immutable `label` to roll back to by name (ocel rollback --tag)")
	cmd.Flags().BoolVar(&opts.Prebuilt, "prebuilt", false, prebuiltFlagUsage)
	commands.AddDryFlag(cmd, &opts.Dry, dryFlagUsage)
	commands.AddNoWaitFlag(cmd, &opts.NoWait)

	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}
