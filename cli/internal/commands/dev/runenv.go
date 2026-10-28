package dev

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/dev"
)

func runDeployed(cmd *cobra.Command, dependencies Dependencies, args []string, name string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	env, err := commands.ResolveDeployedEnvironment(cmd.Context(), cwd, name, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
	if err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(cmd.Context(), cwd)
	if err != nil {
		return err
	}
	opts := dev.Options{
		Command:         args,
		Stdin:           cmd.InOrStdin(),
		Stdout:          cmd.OutOrStdout(),
		Stderr:          cmd.ErrOrStderr(),
		StdinIsTerminal: dependencies.StdinIsTerminal(cmd.InOrStdin()),
		RecordEvent:     dependencies.RecordEvent,
	}
	return dependencies.WithProvider(cmd.Context(), cfg, "ocel run", commands.OpenOptions{}, func(ctx context.Context, p commands.ProviderRun) error {
		opts.Run = p.Run
		opts.Project = p.Project
		return dev.RunDeployed(ctx, opts, dev.Deployed{
			Provider:            p.Provider,
			Check:               p.Check,
			Environment:         env,
			CollectDeclarations: dependencies.CollectDeclarations,
		})
	})
}
