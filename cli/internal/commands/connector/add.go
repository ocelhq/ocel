package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/run"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/connectorserver"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func explainUnfinishedAdd(err error) error {
	return fmt.Errorf("%w; the console already has this target registered, so running ocel connector add again finishes it", err)
}

func runAdd(ctx context.Context, dependencies Dependencies, cfg *project.Project, link *console.Link, opts options) (err error) {
	vendor, err := requireProviderID(ctx, dependencies, cfg, "ocel connector add")
	if err != nil {
		return err
	}
	access, err := readAccessToken(dependencies)
	if err != nil {
		return err
	}

	ctx, running, err := dependencies.Events.Begin(ctx, "ocel connector add", cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	provider, described, err := reachTarget(ctx, dependencies, cfg, check)
	check.End(err)
	if err != nil {
		return err
	}
	defer provider.Close()

	provisioning := running.Phase(progressv1.Phase_PHASE_PROVISION)
	addingCtx, adding := provisioning.Child(ctx, cfg.Slug, progress.Adding.Title("this target to the console"))
	registered, err := opts.console.UpsertConnector(addingCtx, access, described.GetTargetFingerprint(), vendor)
	adding.End(err)
	if err != nil {
		return fmt.Errorf("record this target in the console: %w", err)
	}

	platform := executables.Platform{GOOS: described.GetOs(), GOARCH: described.GetArch()}
	loadingCtx, loading := provisioning.Child(ctx, cfg.Slug, progress.Loading.Title(fmt.Sprintf("the connector built for %s/%s", platform.GOOS, platform.GOARCH)))
	binary, err := executables.EnsureConnector(loadingCtx, cfg.Dir, vendor, platform)
	loading.End(err)
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

	installed, err := providerprocess.Stream(run.ContextWithSpan(ctx, provisioning), provider, "InstallConnector", &contractv1.InstallConnectorRequest{
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

	addressingCtx, addressing := provisioning.Child(ctx, cfg.Slug, progress.Updating.Title("the console with this connector's address"))
	paired, err := opts.console.SetConnectorAddress(addressingCtx, access, registered.GetId(), console.ConnectorAddress{
		URL:       at.GetUrl(),
		PublicKey: at.GetPublicKey(),
		Compute:   at.GetCompute(),
	})
	addressing.End(err)
	if err != nil {
		return explainUnfinishedAdd(fmt.Errorf("tell the console where to dial this connector: %w", err))
	}

	provisioning.Say(grantsLine(opts.grants()))
	provisioning.End(nil)
	running.Succeed(fmt.Sprintf("Installed the connector on %s, which the console dials at %s", paired.GetTarget(), at.GetUrl()))
	return nil
}

func grantsLine(grants []string) string {
	if len(grants) == 0 {
		return "The console may use this connector for nothing yet"
	}
	return "The console may use this connector for " + english.And(slices.Sorted(slices.Values(grants)))
}
