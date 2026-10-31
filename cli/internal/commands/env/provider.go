package env

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func withCommand(cmd *cobra.Command, dependencies Dependencies, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	return run(cmd.Context(), cwd)
}

type variablesKeyOffer struct{ stdin io.Reader }

func withEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []scopev1.Scope, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, scopes, nil, stderr, drive)
}

func withEnvProviderOfferingVariablesKey(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []scopev1.Scope, stdin io.Reader, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, scopes, &variablesKeyOffer{stdin: stdin}, stderr, drive)
}

func revealScopes(opts envOptions) []scopev1.Scope {
	if opts.reveal {
		return []scopev1.Scope{scopev1.Scope_SCOPE_VARIABLES_REVEAL}
	}
	return nil
}

func runWithEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []scopev1.Scope, keyOffer *variablesKeyOffer, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	target, err := dependencies.ResolveTarget(ctx, cwd)
	if err != nil {
		return err
	}
	cfg := target.Project
	open := commands.OpenOptions{Intent: commands.UseInfrastructure, Tier: opts.tier(), Cloud: target.Cloud, CloudScopes: append([]scopev1.Scope{scopev1.Scope_SCOPE_VARIABLES_READ}, scopes...)}
	if keyOffer != nil {
		open.Intent = commands.UseVariablesKey
		open.Policy = dependencies.Policy(command, false, stderr, keyOffer.stdin)
	}
	return dependencies.WithProvider(ctx, cfg, command, open, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider, status := p.Run, p.Check, p.Provider, p.Preflight
		check.End(nil)
		return drive(ctx, run, provider, cfg, status.Response)
	})
}

func runInSpan(ctx context.Context, running *run.Run, phase progressv1.Phase, cfg *project.Project, title progress.Title, work func(context.Context) error) error {
	root := running.Phase(phase)
	spanCtx, span := root.Child(ctx, cfg.Slug, title)
	err := work(spanCtx)
	span.End(err)
	root.End(err)
	return err
}
