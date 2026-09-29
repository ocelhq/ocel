package env

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func withCommand(cmd *cobra.Command, deps cmddeps.Deps, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	return run(cmd.Context(), cwd)
}

type variablesKeyOffer struct{ stdin io.Reader }

func withEnvProvider(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, command string, stderr io.Writer, drive func(context.Context, *run.Run, *providerclient.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, deps, cwd, opts, command, nil, stderr, drive)
}

func withEnvProviderOfferingVariablesKey(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, command string, stdin io.Reader, stderr io.Writer, drive func(context.Context, *run.Run, *providerclient.Provider, *project.Project, *contractv1.PreflightResponse) error) error {
	return runWithEnvProvider(ctx, deps, cwd, opts, command, &variablesKeyOffer{stdin: stdin}, stderr, drive)
}

func runWithEnvProvider(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, command string, keyOffer *variablesKeyOffer, stderr io.Writer, drive func(context.Context, *run.Run, *providerclient.Provider, *project.Project, *contractv1.PreflightResponse) error) (err error) {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	status, err := preflightEnvProvider(ctx, deps, check, prov, cfg, opts, keyOffer, stderr)
	check.End(err)
	if err != nil {
		return err
	}
	return drive(ctx, run, prov, cfg, status)
}

func preflightEnvProvider(ctx context.Context, deps cmddeps.Deps, check *run.Span, prov *providerclient.Provider, cfg *project.Project, opts envOptions, keyOffer *variablesKeyOffer, stderr io.Writer) (*contractv1.PreflightResponse, error) {
	status, err := preflight.Run(ctx, check, prov, cfg, opts.tier(), "", nil, nil, "ocel bootstrap "+bootstrap.Name(opts.tier()))
	if err != nil || keyOffer == nil {
		return status, err
	}
	return status, offerVariablesKey(ctx, deps, check, prov, cfg, opts, status.GetBootstrap(), keyOffer.stdin, stderr)
}

func offerVariablesKey(ctx context.Context, deps cmddeps.Deps, check *run.Span, prov *providerclient.Provider, cfg *project.Project, opts envOptions, status *contractv1.BootstrapStatus, stdin io.Reader, stderr io.Writer) error {
	front := cfg.EdgeSelection()
	offered, err := bootstrap.Offers(ctx, prov, opts.tier(), front, provider.FeatureVarsKey)
	if err != nil || !offered {
		return err
	}
	plan := bootstrap.PlanOnly(status, provider.FeatureVarsKey)
	return bootstrap.OfferPlan(ctx, check, prov, plan, opts.tier(), front,
		deps.StdinIsTerminal(stdin), stderr, stdin)
}
