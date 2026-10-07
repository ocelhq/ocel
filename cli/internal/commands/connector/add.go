package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/english"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/connectorserver"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func explainUnfinishedAdd(err error) error {
	return fmt.Errorf("%w; the console already has this target registered, so running ocel connector add again finishes it", err)
}

func runAdd(ctx context.Context, dependencies Dependencies, cfg *project.Project, link *console.Link, opts options) (err error) {
	vendor, err := requireProviderID(cfg)
	if err != nil {
		return err
	}
	access, err := readAccessToken(dependencies)
	if err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel connector add", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	provider, described, err := reachTarget(ctx, dependencies, cfg, check)
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

	registered, err := opts.console.UpsertConnector(ctx, access, described.GetTargetFingerprint(), vendor)
	if err != nil {
		return fmt.Errorf("record this target in the console: %w", err)
	}

	platform := executables.Platform{GOOS: described.GetOs(), GOARCH: described.GetArch()}
	binary, err := executables.EnsureConnector(ctx, cfg.Dir, vendor, platform)
	if err != nil {
		return err
	}
	if len(binary) > providerprocess.MaxMessageBytes {
		return fmt.Errorf("the %s connector built for %s/%s is %d bytes, over the %d the provider channel accepts in one message",
			vendor, platform.GOOS, platform.GOARCH, len(binary), providerprocess.MaxMessageBytes)
	}
	config, err := json.Marshal(connectorserver.Config{
		Console:        opts.apiURL,
		ConnectorID:    registered.GetId(),
		OrganizationID: link.OrganizationID,
		Target:         described.GetTargetFingerprint(),
		Grants:         opts.grants(),
	})
	if err != nil {
		return err
	}

	installed, err := providerprocess.Stream(ctx, provider, "InstallConnector", &contractv1.InstallConnectorRequest{
		Binary:     binary,
		Version:    version.Version,
		ConfigJson: config,
		Compute:    opts.compute,
	}, contractv1connect.ProviderServiceClient.InstallConnector)
	if err != nil {
		return explainUnfinishedAdd(err)
	}
	at := installed.GetConnector()
	if at.GetUrl() == "" {
		return explainUnfinishedAdd(errors.New(
			"the provider installed the connector and named no address, so the console has nothing to dial"))
	}

	paired, err := opts.console.SetConnectorAddress(ctx, access, registered.GetId(), console.ConnectorAddress{
		URL:       at.GetUrl(),
		PublicKey: at.GetPublicKey(),
		Compute:   at.GetCompute(),
	})
	if err != nil {
		return explainUnfinishedAdd(fmt.Errorf("tell the console where to dial this connector: %w", err))
	}

	granting := run.Phase(progressv1.Phase_PHASE_PROVISION)
	granting.Say(grantsLine(opts.grants()))
	granting.End(nil)
	run.Succeed(fmt.Sprintf("Installed the connector on %s, which the console dials at %s", paired.GetTarget(), at.GetUrl()))
	return nil
}

func grantsLine(grants []string) string {
	if len(grants) == 0 {
		return "The console may use this connector for nothing yet"
	}
	return "The console may use this connector for " + english.And(slices.Sorted(slices.Values(grants)))
}
