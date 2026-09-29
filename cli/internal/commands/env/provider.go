package env

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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

func runWithEnvProvider(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, command string, keyOffer *variablesKeyOffer, stderr io.Writer, drive func(context.Context, *run.Run, *providerprocess.Provider, *project.Project, *contractv1.PreflightResponse) error) (err error) {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerprocess.Start(ctx, cfg, check, dependencies.Questions, executables.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	status, err := preflightEnvProvider(ctx, dependencies, check, prov, cfg, opts, keyOffer, stderr)
	check.End(err)
	if err != nil {
		return err
	}
	return drive(ctx, run, prov, cfg, status)
}

func preflightEnvProvider(ctx context.Context, dependencies Dependencies, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, opts envOptions, keyOffer *variablesKeyOffer, stderr io.Writer) (*contractv1.PreflightResponse, error) {
	status, err := readiness.Check(ctx, check, prov, cfg, readiness.Request{Tier: opts.tier(), Require: readiness.Infrastructure})
	if err != nil || keyOffer == nil {
		return status, err
	}
	return status, offerVariablesKey(ctx, dependencies, check, prov, cfg, opts, status.GetBootstrap(), keyOffer.stdin, stderr)
}

func offerVariablesKey(ctx context.Context, dependencies Dependencies, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, opts envOptions, status *contractv1.BootstrapStatus, stdin io.Reader, stderr io.Writer) error {
	front := cfg.EdgeSelection()
	offered, err := readiness.HasOffer(ctx, prov, opts.tier(), front, provider.FeatureVarsKey)
	if err != nil || !offered {
		return err
	}
	gap := readiness.NewFeatureGap(status, provider.FeatureVarsKey)
	return readiness.OfferRepair(ctx, check, prov, gap, opts.tier(), front,
		dependencies.StdinIsTerminal(stdin), stderr, stdin)
}
