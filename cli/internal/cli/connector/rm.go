package connector

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runRemove(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, _ *consolelink.Link,
	opts options, stdout, stderr io.Writer) error {
	if _, err := vendored(cfg); err != nil {
		return err
	}
	access, err := token(deps)
	if err != nil {
		return err
	}

	fingerprint, unreached := taken(ctx, deps, cfg, opts, stdout, stderr)
	if fingerprint == "" {
		if opts.target == "" {
			return fmt.Errorf("%w\n\nThe console still holds this target; nothing was forgotten. Reach the machine and run this again, or read the fingerprint with `ocel connector status` and run `ocel connector rm --target <fingerprint>` to forget it in the console alone", unreached)
		}
		return forgotten(ctx, opts, access, opts.target, stdout)
	}

	held, err := opts.console.ByTarget(ctx, access, fingerprint)
	if err != nil {
		return err
	}
	if held == nil {
		fmt.Fprintf(stdout, "%s The console holds no connector for %s\n", check, bold(fingerprint))
		return nil
	}
	if err := opts.console.Remove(ctx, access, held.ID); err != nil {
		return fmt.Errorf("forget this connector in the console: %w", err)
	}
	if unreached != nil {
		fmt.Fprintf(stdout, "%s The console has forgotten %s; the machine did not finish taking the connector off, so what is on it stays: %v\n",
			check, bold(fingerprint), unreached)
		return nil
	}
	fmt.Fprintf(stdout, "%s Connector off %s, and the console has forgotten it\n", check, bold(fingerprint))
	return nil
}

func forgotten(ctx context.Context, opts options, access, fingerprint string, stdout io.Writer) error {
	held, err := opts.console.ByTarget(ctx, access, fingerprint)
	if err != nil {
		return err
	}
	if held == nil {
		fmt.Fprintf(stdout, "%s The console holds no connector for %s\n", check, bold(fingerprint))
		return nil
	}
	if err := opts.console.Remove(ctx, access, held.ID); err != nil {
		return fmt.Errorf("forget this connector in the console: %w", err)
	}
	fmt.Fprintf(stdout, "%s The console has forgotten %s; the target itself was never reached, so what is on it stays\n",
		check, bold(fingerprint))
	return nil
}

func taken(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, opts options,
	stdout, stderr io.Writer) (string, error) {
	if opts.target != "" {
		return "", fmt.Errorf("this run names a target, so the machine behind %s was never asked", opts.target)
	}
	var fingerprint string
	err := provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		described, err := client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		if err != nil {
			return err
		}
		fingerprint = described.GetTargetFingerprint()
		return provider.Stream(ctx, runner, "RemoveConnector", &contractv1.RemoveConnectorRequest{},
			contractv1connect.ProviderServiceClient.RemoveConnector, func(ev *progressv1.OperationEvent) {
				if said := ev.GetProgress().GetMessage(); said != "" {
					fmt.Fprintf(stdout, "  %s\n", said)
				}
			})
	})
	return fingerprint, err
}
