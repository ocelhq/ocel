package dev

import (
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/dev"
)

func NewRunCommand(dependencies Dependencies) *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "run [--env <environment>] -- <command> [args...]",
		Short: "Run a one-off command with your project's resource connections",
		Long: "Run a one-off command with your project's resource connections.\n\n" +
			"Without --env the connections are the dev resources of `ocel dev`. With --env they are the " +
			"resources of a deployed environment, reached over the same port forwards `ocel deploy` opens for " +
			"lifecycle.preBuild: `postgres(\"main\").connectionString` is then the deployed database, for " +
			"a migration or a one-off query, until the command exits. The command is handed the bindings alone, " +
			"not the project's variables.",
		Example: "  $ ocel run -- pnpm test\n" +
			"  $ ocel run --env production -- pnpm drizzle-kit migrate",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if env != "" {
				return runDeployed(cmd, dependencies, args, env)
			}
			return runOnBus(cmd, dependencies, "ocel run", args, dev.RunOnce)
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Run against a deployed `environment` instead of dev resources: production, or preview for the current branch's preview")
	return commands.DeclareMutating(commands.ShareTerminalWithChild(cmd))
}
