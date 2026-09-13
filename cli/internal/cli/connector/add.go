package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	consoleconnector "github.com/ocelhq/ocel/cli/internal/console/connector"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/connectorkit"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func unfinished(err error) error {
	return fmt.Errorf("%w; the console already holds this target, so running ocel connector add again finishes it", err)
}

func runAdd(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, link *consolelink.Link,
	opts options, stdout, stderr io.Writer) error {
	vendor, err := vendored(cfg)
	if err != nil {
		return err
	}
	access, err := token(deps)
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		described, err := client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		if err != nil {
			return err
		}

		held, err := opts.console.Upsert(ctx, access, consoleconnector.Upsert{
			Target: described.GetTargetFingerprint(),
			Vendor: vendor,
			Reach:  reachDial,
		})
		if err != nil {
			return fmt.Errorf("record this target in the console: %w", err)
		}

		binary, err := provider.Connector(ctx, cfg.Dir, vendor, providers.Platform{GOOS: "linux", GOARCH: described.GetArch()})
		if err != nil {
			return err
		}
		config, err := json.Marshal(connectorkit.Config{
			Console:        opts.apiURL,
			ConnectorID:    held.ID,
			OrganizationID: link.OrganizationID,
			Target:         described.GetTargetFingerprint(),
			Grants:         opts.grants(),
		})
		if err != nil {
			return err
		}

		var at *progressv1.ConnectorInstalled
		err = provider.Stream(ctx, runner, "InstallConnector", &contractv1.InstallConnectorRequest{
			Binary:     binary,
			Version:    version.Version,
			ConfigJson: config,
			Compute:    opts.compute,
		}, contractv1connect.ProviderServiceClient.InstallConnector, func(ev *progressv1.OperationEvent) {
			if said := ev.GetProgress().GetMessage(); said != "" {
				fmt.Fprintf(stdout, "  %s\n", said)
			}
			if result := ev.GetResult(); result.GetSuccess() {
				at = result.GetConnector()
			}
		})
		if err != nil {
			return unfinished(err)
		}
		if at.GetUrl() == "" {
			return unfinished(errors.New(
				"the provider installed the connector and named no address, so the console has nothing to dial"))
		}

		paired, err := opts.console.Address(ctx, access, held.ID, consoleconnector.Address{
			URL:       at.GetUrl(),
			PublicKey: at.GetPublicKey(),
			Compute:   at.GetCompute(),
		})
		if err != nil {
			return unfinished(fmt.Errorf("tell the console where to dial this connector: %w", err))
		}

		fmt.Fprintf(stdout, "%s Connector on %s, dialled at %s\n", check, bold(paired.Target), at.GetUrl())
		fmt.Fprintf(stdout, "  the console may %s\n", listed(opts.grants()))
		return nil
	})
}
