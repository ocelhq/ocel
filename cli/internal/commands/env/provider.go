package env

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
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

func withEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []cloudv1.Scope, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, scopes, nil, stderr, drive)
}

func withEnvProviderOfferingVariablesKey(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []cloudv1.Scope, stdin io.Reader, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, scopes, &variablesKeyOffer{stdin: stdin}, stderr, drive)
}

func revealScopes(opts envOptions) []cloudv1.Scope {
	if opts.reveal {
		return []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_REVEAL}
	}
	return nil
}

func runWithEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, scopes []cloudv1.Scope, keyOffer *variablesKeyOffer, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	target, err := dependencies.ResolveTarget(ctx, cwd)
	if err != nil {
		return err
	}
	cfg := target.Project
	open := commands.OpenOptions{Intent: commands.UseInfrastructure, Tier: opts.tier(), Cloud: target.Cloud, CloudScopes: scopes}
	if keyOffer != nil {
		open.Intent = commands.UseVariablesKey
		open.Policy = consent.NewPolicy(command, false, dependencies.CanAsk(keyOffer.stdin), stderr, keyOffer.stdin)
	}
	return dependencies.WithProvider(ctx, cfg, command, open, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider, status := p.Run, p.Check, p.Provider, p.Preflight
		check.End(nil)
		return drive(ctx, run, provider, cfg, status.Response)
	})
}
