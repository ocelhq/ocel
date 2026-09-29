package connector

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func runStatus(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, _ *console.Link,
	opts options, stdout io.Writer) error {
	access, err := token(deps)
	if err != nil {
		return err
	}
	registered, err := opts.console.ListConnectors(ctx, access)
	if err != nil {
		return err
	}

	if deps.ConfigPath() != "" {
		fingerprint, err := fingerprinted(ctx, deps, cfg)
		if err != nil {
			return err
		}
		registered = slices.DeleteFunc(registered, func(row console.Connector) bool { return row.Target != fingerprint })
		if len(registered) == 0 {
			fmt.Fprintf(stdout, "The console has no connector registered for %s. Run `ocel connector add` to put one there.\n", bold(fingerprint))
			return nil
		}
	}
	if len(registered) == 0 {
		fmt.Fprintln(stdout, "This organization has no connector. Run `ocel connector add` against a bootstrapped target to add one.")
		return nil
	}

	slices.SortFunc(registered, func(a, b console.Connector) int {
		if a.Target < b.Target {
			return -1
		}
		if a.Target > b.Target {
			return 1
		}
		return 0
	})
	for at, row := range registered {
		if at > 0 {
			fmt.Fprintln(stdout)
		}
		printed(stdout, row, row.Liveness())
	}
	return nil
}

func fingerprinted(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config) (fingerprint string, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return "", err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel connector status", cfg.Dir)
	if err != nil {
		return "", err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, described, err := reachTarget(ctx, deps, cfg, check)
	check.End(err)
	if err != nil {
		return "", err
	}
	prov.Close()
	return described.GetTargetFingerprint(), nil
}
