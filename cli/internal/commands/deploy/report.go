package deploy

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func appsDeployed(cfg *project.Project, manifest *contractv1.Manifest, results []*progressv1.AppResult, env *environmentv1.Environment) []*consolev1.App {
	apps, _ := deployreport.AppsDeployed(manifest, results, env.GetTier(), func(app string) (string, error) {
		id, _ := build.FrameworkBuildID(cfg.Dir, app)
		return id, nil
	})
	return apps
}

func reportDeployed(cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment, attempt *deployreport.Attempt, out deployOutcome, tag string) (*consolev1.Deployment, error) {
	apps, err := deployreport.AppsDeployed(manifest, out.apps, env.GetTier(), func(app string) (string, error) {
		return build.FrameworkBuildID(cfg.Dir, app)
	})
	if err != nil {
		return nil, err
	}
	deployment := attempt.Succeeded(time.Now(), apps, &consolev1.Promotion{Id: out.promotionID, Seq: out.promotedAt, Tag: tag})
	return deployment, deployreport.Write(cfg.Dir, deployment)
}
