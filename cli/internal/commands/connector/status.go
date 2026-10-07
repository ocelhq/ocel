package connector

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
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
		registered = slices.DeleteFunc(registered, func(row *consolev1.Connector) bool { return row.GetTarget() != fingerprint })
		if len(registered) == 0 && !asJSON {
			fmt.Fprintf(stdout, "The console has no connector registered for %s. Run `ocel connector add` to put one there.\n", terminal.PaletteFor(stdout).Bold(fingerprint))
			return nil
		}
	}
	if len(registered) == 0 && !asJSON {
		fmt.Fprintln(stdout, "This organization has no connector. Run `ocel connector add` against a bootstrapped target to add one.")
		return nil
	}

	slices.SortFunc(registered, func(a, b *consolev1.Connector) int {
		return strings.Compare(a.GetTarget(), b.GetTarget())
	})
	if asJSON {
		return terminal.WriteResultJSON(stdout, connectorStatusResult(registered))
	}
	for at, row := range registered {
		if at > 0 {
			fmt.Fprintln(stdout)
		}
		printConnector(stdout, row, console.LivenessOf(row))
	}
	return nil
}

func connectorStatusResult(registered []*consolev1.Connector) *resultv1.ConnectorStatusResult {
	result := &resultv1.ConnectorStatusResult{Connectors: make([]*resultv1.ConnectorStatus, 0, len(registered))}
	for _, row := range registered {
		result.Connectors = append(result.Connectors, &resultv1.ConnectorStatus{
			Target:       row.GetTarget(),
			Vendor:       row.GetVendor(),
			Compute:      console.ComputeNameOf(row.GetCompute()),
			Reach:        reachName(row.GetReach()),
			Url:          row.GetUrl(),
			Version:      row.GetVersion(),
			Capabilities: sortedCapabilities(row.GetCapabilities()),
			Liveness:     connectorLiveness(console.LivenessOf(row)),
			ConnectedAt:  terminal.FormatRFC3339(timeOf(row.GetConnectedAt())),
			LastSeenAt:   terminal.FormatRFC3339(timeOf(row.GetLastSeenAt())),
			LastDenied:   connectorDenial(row.GetLastDenied()),
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

func connectorDenial(denied *consolev1.ConnectorDenial) *resultv1.ConnectorDenial {
	if denied == nil {
		return nil
	}
	return &resultv1.ConnectorDenial{Verb: denied.GetVerb(), At: terminal.FormatRFC3339(timeOf(denied.GetAt())), Message: denied.GetMessage()}
}

func sortedCapabilities(values []string) []string {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted
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
