package connector

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runRemove(ctx context.Context, dependencies Dependencies, cfg *project.Project, _ *console.Link, opts options) (err error) {
	if _, err := vendored(cfg); err != nil {
		return err
	}
	access, err := token(dependencies)
	if err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel connector rm", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	fingerprint, unreached := taken(ctx, dependencies, run, cfg, opts)
	if fingerprint == "" {
		if opts.target == "" {
			return fmt.Errorf("%w\n\nThe console still has this target registered; nothing was forgotten. Reach the machine and run this again, or read the fingerprint with `ocel connector status` and run `ocel connector rm --target <fingerprint>` to forget it in the console alone", unreached)
		}
		return forgotten(ctx, run, opts, access, opts.target)
	}

	registered, err := opts.console.FindConnector(ctx, access, fingerprint)
	if err != nil {
		return err
	}
	if registered == nil {
		run.Succeed(fmt.Sprintf("The console has no connector registered for %s", fingerprint))
		return nil
	}
	if err := opts.console.RemoveConnector(ctx, access, registered.ID); err != nil {
		return fmt.Errorf("forget this connector in the console: %w", err)
	}
	if unreached != nil {
		run.Succeed(fmt.Sprintf("The console has forgotten %s; the machine did not finish taking the connector off, so what is on it stays: %v", fingerprint, unreached))
		return nil
	}
	run.Succeed(fmt.Sprintf("Connector off %s, and the console has forgotten it", fingerprint))
	return nil
}

func forgotten(ctx context.Context, run *run.Run, opts options, access, fingerprint string) error {
	registered, err := opts.console.FindConnector(ctx, access, fingerprint)
	if err != nil {
		return err
	}
	if registered == nil {
		run.Succeed(fmt.Sprintf("The console has no connector registered for %s", fingerprint))
		return nil
	}
	if err := opts.console.RemoveConnector(ctx, access, registered.ID); err != nil {
		return fmt.Errorf("forget this connector in the console: %w", err)
	}
	run.Succeed(fmt.Sprintf("The console has forgotten %s; the target itself was never reached, so what is on it stays", fingerprint))
	return nil
}

func taken(ctx context.Context, dependencies Dependencies, run *run.Run, cfg *project.Project, opts options) (string, error) {
	if opts.target != "" {
		return "", fmt.Errorf("this run names a target, so the machine behind %s was never asked", opts.target)
	}
	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, described, err := reachTarget(ctx, dependencies, cfg, check)
	check.End(err)
	if err != nil {
		return "", err
	}
	defer provider.Close()

	_, err = providerprocess.Stream(ctx, provider, "RemoveConnector", &contractv1.RemoveConnectorRequest{},
		contractv1connect.ProviderServiceClient.RemoveConnector)
	return described.GetTargetFingerprint(), err
}
