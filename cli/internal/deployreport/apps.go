package deployreport

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func AppsDeployed(manifest *contractv1.Manifest, results []*progressv1.AppResult, tier environmentv1.Tier, frameworkBuildID func(app string) (string, error)) ([]*consolev1.App, error) {
	apps := make([]*consolev1.App, 0, len(manifest.GetApps()))
	var unread []error
	for _, built := range manifest.GetApps() {
		buildID, err := frameworkBuildID(built.GetName())
		if err != nil {
			unread = append(unread, fmt.Errorf("app %s: %w", built.GetName(), err))
		}
		app := &consolev1.App{
			Name:             built.GetName(),
			Folder:           built.GetFolder(),
			Framework:        built.GetFramework().GetName(),
			Compute:          computeKind(provider.ComputeOf(built)),
			BuildId:          built.GetBuildId(),
			HealthPath:       built.GetContainer().GetHealthCheckPath(),
			FrameworkBuildId: buildID,
			Outcome:          consolev1.AppOutcome_APP_OUTCOME_SKIPPED,
		}
		for _, domains := range built.GetDomains() {
			if domains.GetTier() == tier {
				app.Hostnames = domains.GetHostnames()
			}
		}
		if result := slices.IndexFunc(results, func(r *progressv1.AppResult) bool { return r.GetApp() == built.GetName() }); result >= 0 {
			applyResult(app, results[result])
		}
		apps = append(apps, app)
	}
	return apps, errors.Join(unread...)
}

func applyResult(app *consolev1.App, result *progressv1.AppResult) {
	app.Urls = result.GetUrls()
	app.Release = result.GetRelease()
	app.StoragePrefix = result.GetStoragePrefix()
	app.Error = result.GetError()
	switch result.GetOutcome() {
	case progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED:
		app.Outcome = consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED
		if app.Release == "" || app.BuildId == "" {
			app.Outcome = consolev1.AppOutcome_APP_OUTCOME_FAILED
			app.Error = "the provider named no release for this app"
		}
	case progressv1.AppOutcome_APP_OUTCOME_FAILED:
		app.Outcome = consolev1.AppOutcome_APP_OUTCOME_FAILED
	}
}

func AppsRolledBack(cfg *project.Project, releases map[string]string) ([]*consolev1.App, error) {
	apps := make([]*consolev1.App, 0, len(releases))
	var unparsed []error
	for _, name := range slices.Sorted(maps.Keys(releases)) {
		release, err := provider.ParseRelease(releases[name])
		if err != nil {
			unparsed = append(unparsed, fmt.Errorf("app %s: %w", name, err))
			continue
		}
		at := slices.IndexFunc(cfg.Apps, func(a project.App) bool { return a.Name == name })
		if at < 0 {
			continue
		}
		app := &consolev1.App{
			Name:    name,
			BuildId: release.BuildID(),
			Release: release.String(),
			Compute: computeKind(cfg.Apps[at].Compute),
			Outcome: consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
		}
		if framework := cfg.Apps[at].Framework(); buildoutput.IsKnownFramework(framework) {
			app.Framework = framework
		}
		apps = append(apps, app)
	}
	return apps, errors.Join(unparsed...)
}

func computeKind(compute provider.Compute) consolev1.ComputeKind {
	switch compute {
	case provider.ComputeServerless:
		return consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS
	case provider.ComputeContainer:
		return consolev1.ComputeKind_COMPUTE_KIND_CONTAINER
	}
	return consolev1.ComputeKind_COMPUTE_KIND_UNSPECIFIED
}
