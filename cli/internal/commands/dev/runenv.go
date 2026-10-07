package dev

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/dev"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/portforward"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	envProduction = "production"
	envPreview    = "preview"
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
		Project:         cfg,
		Command:         args,
		Stdin:           cmd.InOrStdin(),
		Stdout:          cmd.OutOrStdout(),
		Stderr:          cmd.ErrOrStderr(),
		StdinIsTerminal: dependencies.StdinIsTerminal(cmd.InOrStdin()),
		RecordEvent:     dependencies.RecordEvent,
	}
	return dependencies.WithProvider(cmd.Context(), cfg, "ocel run", commands.OpenOptions{Tier: env.GetTier()}, func(ctx context.Context, p commands.ProviderRun) error {
		opts.Run = p.Run
		cfg := p.Project
		forwards, err := forwardDeployed(ctx, dependencies, p, cfg, env)
		p.Check.End(err)
		if err != nil {
			return err
		}
		opts.Project = cfg
		return errors.Join(dev.RunProjected(ctx, opts, forwards.Bindings(portforward.Project)), forwards.Close())
	})
}

func resolveDeployedEnvironment(dependencies Dependencies, cwd, name string) (*environmentv1.Environment, error) {
	switch name {
	case envProduction:
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED}, nil
	case envPreview:
		return commands.ResolvePreviewEnvironment(cwd, "", environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
	default:
		return nil, fmt.Errorf("--env is %q, want %s or %s", name, envProduction, envPreview)
	}
}

func forwardDeployed(ctx context.Context, dependencies Dependencies, p commands.ProviderRun, cfg *project.Project, env *environmentv1.Environment) (*portforward.Forwards, error) {
	if !p.Provider.Facts().GetForwardsPorts() {
		return nil, fmt.Errorf("the provider of %s forwards no port to a deployed environment, so a command cannot reach its resources from this machine", cfg.Slug)
	}
	resources, err := collectResources(ctx, dependencies, p, cfg)
	if err != nil {
		return nil, err
	}
	infra, err := manifest.AssembleInfra(manifest.InfraInput{
		Project:        cfg,
		Tier:           env.GetTier(),
		Resources:      resources,
		WorkerCeilings: provider.WorkerCeilingsOf(p.Provider.Facts().GetWorkerCeilings()),
	})
	if err != nil {
		return nil, err
	}
	uses := portforward.ProjectUses(infra)
	if len(uses) == 0 {
		p.Check.Say(cfg.Slug + " declares no resource a port can reach, so the command runs without bindings")
		return nil, nil
	}
	names, declared := portforward.Names(uses)
	step := p.Check.Child(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)))
	forwards, err := portforward.Open(ctx, p.Provider, uses, &contractv1.ForwardPortsRequest{Slug: cfg.Slug, Environment: env, Bindings: names})
	step.End(err)
	if err != nil {
		return nil, fmt.Errorf("forward ports to the %s environment of %s: %s", tierName(env), cfg.Slug, portforward.RefusalMessage(err))
	}
	if unforwarded := forwards.Unforwarded(); len(unforwarded) > 0 {
		p.Check.Warn(fmt.Sprintf("The provider forwards no port to %s, so the command goes without its bindings", english.And(unforwarded)))
	}
	return forwards, nil
}

func collectResources(ctx context.Context, dependencies Dependencies, p commands.ProviderRun, cfg *project.Project) ([]declaration.Resource, error) {
	collecting := p.Run.Phase(progressv1.Phase_PHASE_BUILD).Child(cfg.Slug, progress.Collecting.Title("the resources "+cfg.Slug+" declares"))
	said := collecting.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	declarations := variables.NewDeclarations(variables.NoValues{}, variables.Scope{Apps: variablescope.Apps(cfg)})
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, said, said)
	collecting.End(err)
	return resources, err
}

func tierName(env *environmentv1.Environment) string {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
		return envPreview
	}
	return envProduction
}
