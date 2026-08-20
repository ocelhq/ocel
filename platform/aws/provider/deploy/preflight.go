package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
)

type deployPlan struct {
	ready    bool
	apps     []*deploymentsv1.ManifestApp
	consumed map[string]Consumed
	needs    needRecords
	sessions sessionScope
	baked    map[string]appBundle
	builds   appBuilds
}

func planDeploy(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, log func(string)) (deployPlan, error) {
	if cfg.Edge.Facts().RunsCode && cfg.StoreEndpoint == "" {
		return deployPlan{}, fmt.Errorf("no deployments-store worker found for this account; re-run `%s` to provision it before deploying", bootstrapCommand(cfg))
	}

	for _, app := range manifestApps(manifest) {
		if err := naming.ValidateDeploymentID(app.GetDeploymentId()); err != nil {
			return deployPlan{}, fmt.Errorf("app %q: %w; upgrade the CLI so every app is built under one", app.GetName(), err)
		}
	}
	if err := checkStoreSchema(ctx, cfg); err != nil {
		return deployPlan{}, err
	}
	if err := checkMembraneServices(manifest, membrane.Serves); err != nil {
		return deployPlan{}, err
	}
	needs, err := checkNeeds(ctx, cfg, manifest)
	if err != nil {
		return deployPlan{}, err
	}
	consumed, err := consumeLinks(ctx, cfg, manifest, log)
	if err != nil {
		return deployPlan{}, err
	}
	envName, err := EnvName(planEnvironment(cfg))
	if err != nil {
		return deployPlan{}, err
	}
	sessions := newSessionScope(naming.Sanitize(manifest.GetSlug()), envName, cfg.StateTableARN)
	if err := checkInlinePolicyBudget(manifest, consumed, sessions); err != nil {
		return deployPlan{}, err
	}
	if err := checkTagAvailable(ctx, cfg, cfg.Tag); err != nil {
		return deployPlan{}, err
	}

	baked, err := renderAppBundles(cfg, manifest, consumed)
	if err != nil {
		return deployPlan{}, err
	}
	if err := checkISRWriterAgrees(cfg.objectStores(), cfg.isrWriter()); err != nil {
		return deployPlan{}, err
	}
	builds, err := resolveAppBuilds(cfg, manifest, baked)
	if err != nil {
		return deployPlan{}, err
	}

	return deployPlan{
		ready:    true,
		apps:     manifestApps(manifest),
		consumed: consumed,
		needs:    needs,
		sessions: sessions,
		baked:    baked,
		builds:   builds,
	}, nil
}
