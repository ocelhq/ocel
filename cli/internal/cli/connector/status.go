package connector

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	consoleconnector "github.com/ocelhq/ocel/cli/internal/console/connector"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func runStatus(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, _ *consolelink.Link,
	opts options, stdout io.Writer) error {
	access, err := token(deps)
	if err != nil {
		return err
	}
	held, err := opts.console.List(ctx, access)
	if err != nil {
		return err
	}

	if deps.ConfigPath() != "" {
		fingerprint, err := fingerprinted(ctx, deps, cfg, stdout)
		if err != nil {
			return err
		}
		held = slices.DeleteFunc(held, func(row consoleconnector.Connector) bool { return row.Target != fingerprint })
		if len(held) == 0 {
			fmt.Fprintf(stdout, "The console holds no connector for %s. Run `ocel connector add` to put one there.\n", bold(fingerprint))
			return nil
		}
	}
	if len(held) == 0 {
		fmt.Fprintln(stdout, "This organization has no connector. Run `ocel connector add` against a bootstrapped target to add one.")
		return nil
	}

	slices.SortFunc(held, func(a, b consoleconnector.Connector) int {
		if a.Target < b.Target {
			return -1
		}
		if a.Target > b.Target {
			return 1
		}
		return 0
	})
	for at, row := range held {
		if at > 0 {
			fmt.Fprintln(stdout)
		}
		printed(stdout, row, row.Liveness())
	}
	return nil
}

func fingerprinted(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, stdout io.Writer) (string, error) {
	var fingerprint string
	err := provider.Drive(ctx, cfg, stdout, stdout, deps.HostTrust, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		described, err := client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		if err != nil {
			return err
		}
		fingerprint = described.GetTargetFingerprint()
		return nil
	})
	return fingerprint, err
}
