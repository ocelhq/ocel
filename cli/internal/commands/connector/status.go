package connector

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func runStatus(ctx context.Context, dependencies Dependencies, cfg *project.Project, _ *console.Link,
	opts options, stdout io.Writer) error {
	access, err := readAccessToken(dependencies)
	if err != nil {
		return err
	}
	registered, err := opts.console.ListConnectors(ctx, access)
	if err != nil {
		return err
	}
	asJSON := dependencies.Invocation.Presentation(stdout).Format == terminal.FormatJSON

	if dependencies.ConfigPath() != "" {
		fingerprint, err := readFingerprint(ctx, dependencies, cfg)
		if err != nil {
			return err
		}
		registered = slices.DeleteFunc(registered, func(row console.Connector) bool { return row.Target != fingerprint })
		if len(registered) == 0 && !asJSON {
			fmt.Fprintf(stdout, "The console has no connector registered for %s. Run `ocel connector add` to put one there.\n", terminal.PaletteFor(stdout).Bold(fingerprint))
			return nil
		}
	}
	if len(registered) == 0 && !asJSON {
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
	if asJSON {
		return terminal.WriteResultJSON(stdout, connectorStatusResult(registered))
	}
	for at, row := range registered {
		if at > 0 {
			fmt.Fprintln(stdout)
		}
		printConnector(stdout, row, row.Liveness())
	}
	return nil
}

func connectorStatusResult(registered []console.Connector) *resultv1.ConnectorStatusResult {
	result := &resultv1.ConnectorStatusResult{Connectors: make([]*resultv1.ConnectorStatus, 0, len(registered))}
	for _, row := range registered {
		result.Connectors = append(result.Connectors, &resultv1.ConnectorStatus{
			Target:       row.Target,
			Vendor:       row.Vendor,
			Compute:      stringOrEmpty(row.Compute),
			Reach:        row.Reach,
			Url:          stringOrEmpty(row.URL),
			Version:      stringOrEmpty(row.Version),
			Capabilities: sortedCapabilities(row.Capabilities),
			Liveness:     connectorLiveness(row.Liveness()),
			ConnectedAt:  terminal.FormatRFC3339(row.ConnectedAt),
			LastSeenAt:   terminal.FormatRFC3339(row.LastSeenAt),
			LastDenied:   connectorDenial(row.LastDenied),
		})
	}
	return result
}

func connectorLiveness(live console.Liveness) resultv1.ConnectorLiveness {
	switch live {
	case console.Online:
		return resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_ONLINE
	case console.Offline:
		return resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_OFFLINE
	case console.NeverConnected:
		return resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_NEVER_CONNECTED
	default:
		return resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_UNSPECIFIED
	}
}

func connectorDenial(denied *console.Denial) *resultv1.ConnectorDenial {
	if denied == nil {
		return nil
	}
	return &resultv1.ConnectorDenial{Verb: denied.Verb, At: terminal.NormalizeRFC3339(denied.At), Message: denied.Message}
}

func sortedCapabilities(values []string) []string {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func readFingerprint(ctx context.Context, dependencies Dependencies, cfg *project.Project) (fingerprint string, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return "", err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel connector status", cfg.Dir)
	if err != nil {
		return "", err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, described, err := reachTarget(ctx, dependencies, cfg, check)
	check.End(err)
	if err != nil {
		return "", err
	}
	provider.Close()
	return described.GetTargetFingerprint(), nil
}
