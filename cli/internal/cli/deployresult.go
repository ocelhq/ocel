package cli

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func recordDeployResult(cfg *projectconfig.Config, manifest *contractv1.Manifest, env *environmentv1.Environment, tag, promotionID string, appURLs []string) error {
	apps := make([]deployresult.App, 0, len(manifest.GetApps()))
	for _, a := range manifest.GetApps() {
		apps = append(apps, deployresult.App{
			Name:         a.GetName(),
			BuildID:      appbuilder.BuildID(cfg.Dir, a.GetName()),
			DeploymentID: a.GetDeploymentId(),
		})
	}

	if err := deployresult.Write(cfg.Dir, deployresult.Result{
		Slug: cfg.Slug,
		Environment: deployresult.Environment{
			Class:    environmentClassKey(env.GetTier()),
			Identity: env.GetIdentity(),
		},
		PromotionID: promotionID,
		Tag:         tag,
		AppURLs:     appURLs,
		Apps:        apps,
	}); err != nil {
		return fmt.Errorf("write deploy result: %w", err)
	}
	return nil
}

func environmentClassKey(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production"
	case environmentv1.Tier_TIER_PREVIEW:
		return "preview"
	default:
		return "unspecified"
	}
}
