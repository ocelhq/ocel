package connector

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/run"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/scope"
)

func explainUnfinishedAdd(err error) error {
	var refused *clierror.Error
	if errors.As(err, &refused) && refused.Code == clierror.CodeProviderUnsupported {
		return err
	}
	return fmt.Errorf("%w; the console already has this target registered, so running ocel connector add again finishes it", err)
}

func (o options) installedRunner() (*contractv1.ConnectorRunner, error) {
	if !o.runner {
		if o.cpus != "" || o.memory != "" || o.image != "" {
			return nil, &clierror.Error{Code: clierror.CodeUsage, Cause: errors.New("--cpus, --memory and --image shape a runner: add --runner")}
		}
		return nil, nil
	}
	return &contractv1.ConnectorRunner{Cpus: o.cpus, Memory: o.memory, Image: o.image}, nil
}

func runAdd(ctx context.Context, dependencies Dependencies, cfg *project.Project, link *console.Link, opts options) (err error) {
	runner, err := opts.installedRunner()
	if err != nil {
		return err
	}
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
	if runner != nil && !provider.Facts().GetConnectorRunsJobs() {
		return &clierror.Error{Code: clierror.CodeProviderUnsupported, Cause: fmt.Errorf("the %s connector runs no jobs: add it without --runner", vendor)}
	}

	provisioning := running.Phase(progressv1.Phase_PHASE_PROVISION)
	addingCtx, adding := provisioning.Child(ctx, cfg.Slug, progress.Adding.Title("this target to the console"))
	registered, err := opts.console.UpsertConnector(addingCtx, access, cmp.Or(opts.name, console.ConnectorNameOf(described.GetTargetFingerprint())), described.GetTargetFingerprint(), vendor)
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
	config, err := json.Marshal(endpoint.Config{
		Console:        opts.apiURL,
		EndpointID:     registered.GetId(),
		OrganizationID: link.OrganizationID,
		Name:           registered.GetName(),
		Target:         described.GetTargetFingerprint(),
		AllowedScopes:  scope.ClaimsOf(scope.ListUserScopes()),
	})
	if err != nil {
		return err
	}

	installed, err := providerprocess.Stream(run.ContextWithSpan(ctx, provisioning), provider, "InstallConnector", &contractv1.InstallConnectorRequest{
		Binary:     binary,
		Version:    version.Version,
		ConfigJson: config,
		Compute:    opts.compute,
		Runner:     runner,
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

	provisioning.Say(grantsLine(scope.ClaimsOf(scope.ListUserScopes())))
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
