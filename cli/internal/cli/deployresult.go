package cli

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func recordDeployResult(cfg *projectconfig.Config, manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, tag, promotionID string, appURLs []string) error {
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
			Class:    environmentClassKey(env.GetClass()),
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

func environmentClassKey(class deploymentsv1.Environment_Class) string {
	switch class {
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		return "production"
	case deploymentsv1.Environment_CLASS_PREVIEW:
		return "preview"
	case deploymentsv1.Environment_CLASS_DEVELOPMENT:
		return "development"
	default:
		return "unspecified"
	}
}
