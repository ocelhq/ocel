package deployreport

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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
	app.StorageCoordinate = result.GetStorageCoordinate()
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

func AppsRolledBack(cfg *project.Project, results []*progressv1.AppResult) ([]*consolev1.App, error) {
	apps := make([]*consolev1.App, 0, len(results))
	var unparsed []error
	for _, result := range slices.SortedFunc(slices.Values(results), func(a, b *progressv1.AppResult) int { return strings.Compare(a.GetApp(), b.GetApp()) }) {
		name := result.GetApp()
		release, err := provider.ParseRelease(result.GetRelease())
		if err != nil {
			unparsed = append(unparsed, fmt.Errorf("app %s: %w", name, err))
			continue
		}
		at := slices.IndexFunc(cfg.Apps, func(a project.App) bool { return a.Name == name })
		if at < 0 {
			continue
		}
		app := &consolev1.App{
			Name:      name,
			BuildId:   release.BuildID(),
			Release:   release.String(),
			Compute:   computeKind(cfg.Apps[at].Compute),
			Outcome:   consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
			Variables: result.GetVariables(),
			Values:    rolledBackValues(result.GetValues()),
		}
		if framework := cfg.Apps[at].Framework; buildoutput.IsKnownFramework(framework) {
			app.Framework = framework
		}
		apps = append(apps, app)
	}
	return apps, errors.Join(unparsed...)
}

func VariableGroupsRolledBack(results []*progressv1.AppResult) []*resourcesv1.GroupDefinition {
	var groups []*resourcesv1.GroupDefinition
	for _, result := range results {
		for _, group := range result.GetVariableGroups() {
			if !slices.ContainsFunc(groups, func(g *resourcesv1.GroupDefinition) bool { return g.GetKey() == group.GetKey() }) {
				groups = append(groups, group)
			}
		}
	}
	slices.SortFunc(groups, func(a, b *resourcesv1.GroupDefinition) int { return strings.Compare(a.GetKey(), b.GetKey()) })
	return groups
}

func rolledBackValues(values []*progressv1.ReleasedValue) []*consolev1.ReleasedValue {
	resolved := make([]variables.Variable, 0, len(values))
	for _, v := range values {
		resolved = append(resolved, variables.Variable{Key: v.GetKey(), Class: v.GetClass(), Folder: v.GetFolder(), Environment: v.GetEnvironment(), Version: v.GetVersion()})
	}
	return ReleasedValues(resolved)
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
