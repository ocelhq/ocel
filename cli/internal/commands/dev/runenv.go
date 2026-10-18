package dev

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/dev"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func runDeployed(cmd *cobra.Command, dependencies Dependencies, args []string, name string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	env, err := resolveDeployedEnvironment(dependencies, cwd, name)
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

func resolveDeployedEnvironment(dependencies Dependencies, cwd, name string) (*environmentv1.Environment, error) {
	switch name {
	case string(environment.TierProduction):
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED}, nil
	case string(environment.TierPreview):
		return previewid.ResolveEnvironment(cwd, "", environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
	default:
		return nil, fmt.Errorf("--env is %q, want %s or %s", name, environment.TierProduction, environment.TierPreview)
	}
}
