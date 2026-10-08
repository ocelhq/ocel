package deploy

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func lenientFrameworkBuildID(projectDir string) func(app string) (string, error) {
	return func(app string) (string, error) {
		id, _ := build.FrameworkBuildID(projectDir, app)
		return id, nil
	}
}

func reportDeployed(cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment, attempt deployreport.Attempt, out deployOutcome) (*consolev1.Deployment, error) {
	apps, err := deployreport.AppsDeployed(manifest, out.apps, env.GetTier(), func(app string) (string, error) {
		return build.FrameworkBuildID(cfg.Dir, app)
	})
	if err != nil {
		return nil, err
	}
	attempt.Apps, attempt.PromotionID, attempt.PromotionSeq = apps, out.promotionID, out.promotedAt
	deployment := attempt.Succeeded(time.Now())
	return deployment, deployreport.Write(cfg.Dir, deployment)
}

func fileReport(ctx context.Context, dependencies Dependencies, projectDir string, attempt *deployreport.Attempt, report *consolev1.Deployment, runErr error, stderr io.Writer) {
	if report == nil && runErr != nil && attempt != nil {
		report = attempt.Failed(time.Now(), runErr)
	}
	if report != nil {
		dependencies.DeploymentReports.ReportDeployment(ctx, projectDir, report, stderr)
	}
}
