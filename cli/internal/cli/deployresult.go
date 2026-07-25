package cli

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// recordDeployResult writes .ocel/deploy-result.json for a deploy that just
// succeeded, so a later process can consume the outcome without scraping the
// CLI's human-facing output (see package deployresult). The build ids come from
// the build output this same run produced, the deployment id and featured URLs
// from the provider's terminal result.
func recordDeployResult(cfg *projectconfig.Config, manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, tag, deploymentID string, appURLs []string) error {
	apps := make([]deployresult.App, 0, len(manifest.GetApps()))
	for _, a := range manifest.GetApps() {
		apps = append(apps, deployresult.App{Name: a.GetName(), BuildID: appbuilder.BuildID(cfg.Dir, a.GetName())})
	}

	if err := deployresult.Write(cfg.Dir, deployresult.Result{
		ProjectID: cfg.ProjectID,
		Environment: deployresult.Environment{
			Class:    environmentClassKey(env.GetClass()),
			Identity: env.GetIdentity(),
		},
		DeploymentID: deploymentID,
		Tag:          tag,
		AppURLs:      appURLs,
		Apps:         apps,
	}); err != nil {
		return fmt.Errorf("write deploy result: %w", err)
	}
	return nil
}

// clearDeployResult removes a previous run's deploy result before this run
// provisions anything, so a failed deploy can never leave the last successful
// one behind to be read as its own outcome.
func clearDeployResult(cfg *projectconfig.Config) error {
	return deployresult.Clear(cfg.Dir)
}

// environmentClassKey renders an environment class as the lowercase token the
// deploy result and the config's domain block both key by.
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
