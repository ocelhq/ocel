package deployreport

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/project"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func deployedManifest() *contractv1.Manifest {
	return &contractv1.Manifest{Apps: []*contractv1.ManifestApp{
		{
			Name:      "web",
			Framework: &contractv1.Framework{Name: "next"},
			Folder:    "/apps/web",
			BuildId:   "3f7c1b9a5e2d4c8f",
			Domains: []*contractv1.TierDomains{
				{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{"shop.example.com"}},
				{Tier: environmentv1.Tier_TIER_PREVIEW, Hostnames: []string{"*.preview.example.com"}},
			},
			Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
		},
		{
			Name:    "api",
			BuildId: "a1b2c3d4e5f60718",
			Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
				HealthCheckPath: "/healthz",
			}},
		},
	}}
}

func TestADeployedAppRecordsWhatTheManifestBuiltAndTheProviderMadeLive(t *testing.T) {
	t.Parallel()
	results := []*progressv1.AppResult{
		{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Release: webRelease, Urls: []string{"https://shop.example.com"}, StoragePrefix: "prod/shop/web/r0123abcd/"},
		{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED, Release: "a1b2c3d4e5f60718~0123456789ab"},
	}

	apps := mustAppsDeployed(t, deployedManifest(), results, environmentv1.Tier_TIER_PRODUCTION, func(app string) (string, error) { return "fw-" + app, nil })

	want := []*consolev1.App{
		{
			Name: "web", Folder: "/apps/web", Framework: "next",
			Compute: consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS,
			BuildId: "3f7c1b9a5e2d4c8f", Release: webRelease,
			Urls: []string{"https://shop.example.com"}, Hostnames: []string{"shop.example.com"},
			Outcome:          consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
			FrameworkBuildId: "fw-web", StoragePrefix: "prod/shop/web/r0123abcd/",
		},
		{
			Name: "api", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
			BuildId: "a1b2c3d4e5f60718", Release: "a1b2c3d4e5f60718~0123456789ab",
			HealthPath:       "/healthz",
			Outcome:          consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
			FrameworkBuildId: "fw-api",
		},
	}
	if len(apps) != len(want) {
		t.Fatalf("apps = %v, want %d", apps, len(want))
	}
	for i := range want {
		if !proto.Equal(apps[i], want[i]) {
			t.Errorf("app %d = %v, want %v", i, apps[i], want[i])
		}
	}
}

func TestAPreviewAppRecordsThePreviewHostnamesOnly(t *testing.T) {
	t.Parallel()

	apps := mustAppsDeployed(t, deployedManifest(), nil, environmentv1.Tier_TIER_PREVIEW, func(string) (string, error) { return "", nil })

	if !slices.Equal(apps[0].GetHostnames(), []string{"*.preview.example.com"}) {
		t.Errorf("hostnames = %v, want the preview wildcard", apps[0].GetHostnames())
	}
}

func TestAnAppTheProviderNeverReachedIsSkippedAndAFailedOneNamesItsError(t *testing.T) {
	t.Parallel()
	results := []*progressv1.AppResult{
		{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED, Error: "the function was refused"},
		{App: "api", Outcome: progressv1.AppOutcome_APP_OUTCOME_NOT_RUN},
	}

	apps := mustAppsDeployed(t, deployedManifest(), results, environmentv1.Tier_TIER_PRODUCTION, func(string) (string, error) { return "", nil })

	if apps[0].GetOutcome() != consolev1.AppOutcome_APP_OUTCOME_FAILED || apps[0].GetError() != "the function was refused" {
		t.Errorf("web = %v, want failed with the provider's error", apps[0])
	}
	if apps[1].GetOutcome() != consolev1.AppOutcome_APP_OUTCOME_SKIPPED {
		t.Errorf("api outcome = %v, want skipped", apps[1].GetOutcome())
	}
	none := mustAppsDeployed(t, deployedManifest(), nil, environmentv1.Tier_TIER_PRODUCTION, func(string) (string, error) { return "", nil })
	if none[0].GetOutcome() != consolev1.AppOutcome_APP_OUTCOME_SKIPPED {
		t.Errorf("an app with no result has outcome %v, want skipped", none[0].GetOutcome())
	}
}

func TestAnAppSucceedsOnlyWhenItsBuildAndReleaseAreKnown(t *testing.T) {
	t.Parallel()
	results := []*progressv1.AppResult{{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED}}

	apps := mustAppsDeployed(t, deployedManifest(), results, environmentv1.Tier_TIER_PRODUCTION, func(string) (string, error) { return "", nil })

	if apps[0].GetOutcome() != consolev1.AppOutcome_APP_OUTCOME_FAILED {
		t.Errorf("web outcome = %v, want failed: the provider named no release for it", apps[0].GetOutcome())
	}
}

func TestARolledBackPromotionRecordsEachAppAtTheReleaseItWentLiveWith(t *testing.T) {
	t.Parallel()
	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "next"}},
		{Name: "api", Compute: provider.ComputeContainer, Container: &project.Container{}},
	}}

	apps, err := AppsRolledBack(cfg, map[string]string{"web": webRelease, "api": "a1b2c3d4e5f60718~0123456789ab", "removed": "0000000000000000~0123456789ab"})
	if err != nil {
		t.Fatalf("AppsRolledBack() error = %v", err)
	}

	if len(apps) != 2 || apps[0].GetName() != "api" || apps[1].GetName() != "web" {
		t.Fatalf("apps = %v, want api then web, sorted, without removed: the project no longer says what compute it ran on", apps)
	}
	web := apps[1]
	if web.GetBuildId() != "3f7c1b9a5e2d4c8f" || web.GetRelease() != webRelease || web.GetFramework() != "next" {
		t.Errorf("web = %v, want build 3f7c1b9a5e2d4c8f at release %s on next", web, webRelease)
	}
	if web.GetCompute() != consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS || apps[0].GetCompute() != consolev1.ComputeKind_COMPUTE_KIND_CONTAINER {
		t.Errorf("computes = %v and %v, want the project's", web.GetCompute(), apps[0].GetCompute())
	}
	if web.GetOutcome() != consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED {
		t.Errorf("web outcome = %v, want succeeded", web.GetOutcome())
	}
}

func TestARollbackToAReleaseNoBuildNamesIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := AppsRolledBack(&project.Project{}, map[string]string{"web": "no-separator"}); err == nil {
		t.Fatal("AppsRolledBack() = nil, want an error for a release that names no build")
	}
}

func mustAppsDeployed(t *testing.T, manifest *contractv1.Manifest, results []*progressv1.AppResult, tier environmentv1.Tier, frameworkBuildID func(string) (string, error)) []*consolev1.App {
	t.Helper()
	apps, err := AppsDeployed(manifest, results, tier, frameworkBuildID)
	if err != nil {
		t.Fatalf("AppsDeployed() error = %v", err)
	}
	return apps
}

func TestAnAppWhoseFrameworkBuildIDCannotBeReadFailsTheRecord(t *testing.T) {
	t.Parallel()

	_, err := AppsDeployed(deployedManifest(), nil, environmentv1.Tier_TIER_PRODUCTION, func(app string) (string, error) { return "", errors.New("no serve descriptor") })

	if err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("AppsDeployed() error = %v, want one naming the app", err)
	}
}
