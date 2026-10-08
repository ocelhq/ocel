package promotions

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func productionEnvironment() *environmentv1.Environment {
	return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
}

func resolvedProject(p commands.ProviderRun, cfg *project.Project) *project.Project {
	if p.Preflight.Project != nil {
		return p.Preflight.Project
	}
	return cfg
}

func reportRolledBack(attempt *deployreport.Attempt, promoted *contractv1.Promotion) (*consolev1.Deployment, error) {
	apps, err := deployreport.AppsRolledBack(attempt.Project, promoted.GetReleases())
	if err != nil {
		return nil, err
	}
	deployment := attempt.Succeeded(time.Now(), apps, &consolev1.Promotion{Id: promoted.GetPromotionId(), Seq: promoted.GetTs(), Tag: promoted.GetTag()})
	return deployment, deployreport.Write(attempt.Project.Dir, deployment)
}
