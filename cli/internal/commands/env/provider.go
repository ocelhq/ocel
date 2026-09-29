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
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	providercontract "github.com/ocelhq/ocel/pkg/provider"
)

func withCommand(cmd *cobra.Command, dependencies Dependencies, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	return run(cmd.Context(), cwd)
}

type variablesKeyOffer struct{ stdin io.Reader }

func withEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, nil, stderr, drive)
}

func withEnvProviderOfferingVariablesKey(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, stdin io.Reader, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, dependencies, cwd, opts, command, &variablesKeyOffer{stdin: stdin}, stderr, drive)
}

func runWithEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, keyOffer *variablesKeyOffer, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	return dependencies.WithProvider(ctx, cfg, command, commands.OpenOptions{Tier: opts.tier(), Require: readiness.Infrastructure}, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider, status := p.Run, p.Check, p.Provider, p.Preflight
		if keyOffer != nil {
			if err := offerVariablesKey(ctx, dependencies, check, provider, cfg, opts, status.Response.GetBootstrap(), keyOffer.stdin, stderr); err != nil {
				return err
			}
		}
		check.End(nil)
		return drive(ctx, run, provider, cfg, status.Response)
	})
}

func offerVariablesKey(ctx context.Context, dependencies Dependencies, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, opts envOptions, status *contractv1.BootstrapStatus, stdin io.Reader, stderr io.Writer) error {
	edge := cfg.EdgeSelection()
	offered, err := readiness.HasOffer(ctx, provider, opts.tier(), edge, providercontract.FeatureVariablesKey)
	if err != nil || !offered {
		return err
	}
	gap := readiness.NewFeatureGap(status, providercontract.FeatureVariablesKey)
	return readiness.OfferRepair(ctx, check, provider, gap, opts.tier(), edge,
		dependencies.StdinIsTerminal(stdin), stderr, stdin)
}
