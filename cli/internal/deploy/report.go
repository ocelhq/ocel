package deploy

import (
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func appsDeployed(cfg *project.Project, manifest *contractv1.Manifest, results []*progressv1.AppResult, env *environmentv1.Environment) ([]*consolev1.App, error) {
	return deployreport.AppsDeployed(manifest, results, env.GetTier(), func(app string) (string, error) {
		return build.FrameworkBuildID(cfg.Dir, app)
	})
}

func (o deployOutcome) promotion(tag string) *consolev1.Promotion {
	return &consolev1.Promotion{Id: o.promotionID, Seq: o.promotedAt, Tag: tag}
}
