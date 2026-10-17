package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const ocelCloudArch = "amd64"

type ocelCloudTarget struct {
	console *console.Client
	session string
	url     string
}

func findOcelCloud(ctx context.Context, dependencies Dependencies, cfg *project.Project) (ocelCloudTarget, error) {
	credentials, err := dependencies.Console.ReadCredentials()
	if errors.Is(err, console.ErrNotLoggedIn) {
		return ocelCloudTarget{}, refuseNoTarget(cfg, "you're not logged in to a console")
	}
	if err != nil {
		return ocelCloudTarget{}, fmt.Errorf("your saved login could not be read: %w\nRun `ocel login` to sign in again", err)
	}
	apiURL := console.BaseURL(credentials.APIURL)
	client := console.New(apiURL)
	offer, err := client.ReadCloudOffer(ctx, credentials.AccessToken)
	if errors.Is(err, console.ErrCloudNotOffered) {
		return ocelCloudTarget{}, refuseNoTarget(cfg, "the console at "+apiURL+" offers no Ocel Cloud")
	}
	if err != nil {
		return ocelCloudTarget{}, fmt.Errorf("ask the console at %s whether it offers Ocel Cloud: %w", apiURL, err)
	}
	return ocelCloudTarget{console: client, session: credentials.AccessToken, url: offer.URL}, nil
}

func refuseNoTarget(cfg *project.Project, why string) error {
	return &clierror.Error{
		Code: clierror.CodeProjectInvalidConfig,
		Hint: "provider",
		Cause: fmt.Errorf("%s configures no provider, and %s, so this project has nowhere to deploy: add `\"provider\": { \"<id>\": { … } }` keyed by one of %s to deploy into your own account, or run `ocel login` with a console that offers Ocel Cloud",
			filepath.Base(cfg.Path), why, strings.Join(configdoc.ProviderIDs(), ", ")),
	}
}

func refuseFlagsOcelCloudLacks(opts deployOptions) error {
	var given []string
	if opts.dry {
		given = append(given, "--dry")
	}
	if opts.tag != "" {
		given = append(given, "--tag")
	}
	if opts.prebuilt {
		given = append(given, "--prebuilt")
	}
	if len(given) == 0 {
		return nil
	}
	return &clierror.Error{Code: clierror.CodeUsage, Cause: fmt.Errorf("%s deploy only through a provider, and this project names none, so it deploys to Ocel Cloud", strings.Join(given, ", "))}
}

func deployToOcelCloud(ctx context.Context, dependencies Dependencies, cfg *project.Project, opts deployOptions, stderr io.Writer) (err error) {
	if err := refuseFlagsOcelCloudLacks(opts); err != nil {
		return err
	}
	target, err := findOcelCloud(ctx, dependencies, cfg)
	if err != nil {
		return err
	}
	if err := cloud.RefuseAppName(cfg.Slug); err != nil {
		return err
	}
	cfg, err = cfg.ResolveComputes([]string{string(provider.ComputeContainer)}, "Ocel Cloud")
	if err != nil {
		return err
	}
	if cfg.HasJSApp() {
		if err := node.Ensure(cfg.Dir); err != nil {
			return err
		}
	}

	ctx, running, err := dependencies.Events.Begin(ctx, "ocel deploy", cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)
	if err := deployreport.Clear(cfg.Dir); err != nil {
		return err
	}
	trigger, ci := deployreport.ReadTrigger(os.Getenv, dependencies.DiscoverPRNumber())
	attempt := &deployreport.Attempt{
		Kind:        consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY,
		Project:     cfg,
		Provider:    cloud.ProviderName,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Target:      cloud.ConnectorTarget,
		TraceID:     running.TraceID(),
		StartedAt:   running.StartedAt(),
		Trigger:     trigger,
		CI:          ci,
		Source:      deployreport.ReadSource(cfg.Dir, os.Getenv),
	}
	var apps []*consolev1.App
	var succeeded *consolev1.Deployment
	defer func() { dependencies.Console.ReportAttempt(ctx, attempt, apps, succeeded, err, stderr) }()

	phase := running.Phase(progressv1.Phase_PHASE_BUILD)
	assembled, built, err := buildForOcelCloud(ctx, dependencies, cfg, phase)
	phase.End(err)
	if err != nil {
		return err
	}

	shipping := running.Phase(progressv1.Phase_PHASE_DEPLOY)
	deployed, err := shipToOcelCloud(ctx, dependencies, target, cfg, assembled, built, shipping)
	shipping.End(err)
	apps = newConsoleApps(assembled, deployed, err)
	if err != nil {
		return err
	}
	if succeeded, err = deployreport.WriteSucceeded(attempt, apps, &consolev1.Promotion{Id: deployed.Deployment.GetId(), Seq: time.Now().UnixMilli()}, nil); err != nil {
		return err
	}
	running.Succeed(fmt.Sprintf("Deployed %s to Ocel Cloud: %s", cfg.Slug, deployed.URL))
	return nil
}

func buildForOcelCloud(ctx context.Context, dependencies Dependencies, cfg *project.Project, phase *run.Span) (*contractv1.Manifest, build.Output, error) {
	declarations := variables.NewDeclarations(variables.NoValues{}, variables.Scope{Apps: variablescope.Apps(cfg), Tier: environmentv1.Tier_TIER_PRODUCTION})
	said := phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, said, said)
	if err != nil {
		return nil, build.Output{}, err
	}
	placement, err := manifest.PlaceConsumers(cfg, resources)
	if err != nil {
		return nil, build.Output{}, err
	}
	usages, err := manifest.FindUsages(ctx, cfg, resources)
	if err != nil {
		return nil, build.Output{}, err
	}
	archs := make(map[string]string, len(cfg.Apps))
	for _, app := range cfg.Apps {
		archs[app.Name] = ocelCloudArch
	}
	clients := clientenv.AppsOf(cfg, declarations.Definitions(), nil)
	steps := newBuildSteps(phase)
	var built build.Output
	err = steps.run(cfg.Slug, progress.Building.Title(appList(cfg)), func() (err error) {
		built, err = dependencies.BuildApps(ctx, cfg, build.SplitVariablesByClass(clients, nil), archs, placement.HostedWorkers(), build.Host{}, steps.log())
		if err != nil {
			return err
		}
		return clientenv.Record(cfg.Dir, clients)
	})
	if err != nil {
		return nil, build.Output{}, err
	}
	if err := manifest.RefuseUnnamedApps(cfg, built); err != nil {
		return nil, build.Output{}, err
	}
	assembled, err := manifest.Assemble(manifest.Input{
		Project:   cfg,
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Resources: resources,
		Built:     built,
		Usages:    usages,
		BuildID:   dependencies.BuildID,
	})
	return assembled, built, err
}

func shipToOcelCloud(ctx context.Context, dependencies Dependencies, target ocelCloudTarget, cfg *project.Project, assembled *contractv1.Manifest, built build.Output, phase *run.Span) (cloud.Deployed, error) {
	saved, err := os.MkdirTemp("", "ocel-cloud-images-")
	if err != nil {
		return cloud.Deployed{}, err
	}
	defer func() { _ = os.RemoveAll(saved) }()
	images := make(map[string]string, len(built.Images))
	for _, app := range slices.Sorted(maps.Keys(built.Images)) {
		path := filepath.Join(saved, app+".tar")
		if err := saveImage(ctx, dependencies, built.Images[app], path); err != nil {
			return cloud.Deployed{}, fmt.Errorf("save the image of %s: %w", app, err)
		}
		images[app] = path
	}
	var deployed cloud.Deployed
	err = newBuildSteps(phase).run(cfg.Slug, progress.Deploying.Title(cfg.Slug+" to Ocel Cloud"), func() (err error) {
		said := phase.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
		deployed, err = cloud.New(target.url, target.console, target.session).Deploy(ctx, cfg.Slug, assembled, images, said)
		return err
	})
	return deployed, err
}

func saveImage(ctx context.Context, dependencies Dependencies, ref, path string) error {
	repository, digest, found := strings.Cut(ref, "@")
	if !found {
		return fmt.Errorf("%s names no digest, so the image the build made cannot be found", ref)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	saveErr := dependencies.SaveImage(ctx, repository, digest, file)
	return errors.Join(saveErr, file.Close())
}

func newConsoleApps(assembled *contractv1.Manifest, deployed cloud.Deployed, failure error) []*consolev1.App {
	apps := make([]*consolev1.App, 0, len(assembled.GetApps()))
	for _, declared := range assembled.GetApps() {
		app := &consolev1.App{Name: declared.GetName(), Compute: consolev1.ComputeKind_COMPUTE_KIND_MICROVM, BuildId: declared.GetBuildId()}
		apps = append(apps, app)
		if failure != nil {
			app.Outcome = consolev1.AppOutcome_APP_OUTCOME_FAILED
			continue
		}
		release, err := provider.NewRelease(declared.GetBuildId(), deployed.Deployment.GetId(), stackrecords.ProductionEnv, "")
		if err != nil {
			app.Outcome = consolev1.AppOutcome_APP_OUTCOME_FAILED
			app.Error = err.Error()
			continue
		}
		app.Outcome = consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED
		app.Release = release.String()
		if url := deployed.URLs[app.Name]; url != "" {
			app.Urls = []string{url}
		}
	}
	return apps
}
