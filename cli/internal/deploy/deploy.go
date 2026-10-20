package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

type Dependencies struct {
	commands.Invocation
	BuildApps               func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error)
	RefuseUnbuildableImages func(ctx context.Context, span *run.Span, cfg *project.Project, archs map[string]string) error
	ReadPrebuilt            func(ctx context.Context, cfg *project.Project, archs map[string]string) (build.Output, error)
	BuildID                 func(projectDir, app string) (string, error)
	CollectDeclarations     func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
	OpenBrowser             func(url string) error
	ServeVariableEditor     func(ctx context.Context, cfg *project.Project, provider *providerprocess.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error)
	ReadGitBranch           func(dir string) (string, error)
	DiscoverPRNumber        func() string
}

type Options struct {
	Yes      bool
	Dry      bool
	Prebuilt bool
}

func To(ctx context.Context, dependencies Dependencies, cwd string, target Target, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	resolved, err := dependencies.ResolveTarget(ctx, cwd)
	if err != nil {
		return err
	}
	if resolved.IsCloud() {
		return toCloud(ctx, dependencies, resolved, target, opts)
	}
	policy, cfg, err := ensureProject(ctx, dependencies, target.command(), cwd, resolved.Project, opts.Yes, opts.Dry, stdout, stdin)
	if err != nil || cfg == nil {
		return err
	}
	env, err := target.environment(dependencies, cwd)
	if err != nil {
		return err
	}

	if !opts.Dry {
		if err := deployreport.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	deployTelemetry := watchDeploy(dependencies.Events, cfg, target.telemetryTarget(), opts.Dry)
	var attempt *deployreport.Attempt
	var apps []*consolev1.App
	var succeeded *consolev1.Deployment
	err = dependencies.WithProvider(ctx, cfg, target.command(), commands.OpenOptions{Intent: commands.Deploy, Tier: target.tier, Policy: policy, DryRun: policy.DryRun}, func(ctx context.Context, p commands.ProviderRun) (err error) {
		run, check, provider := p.Run, p.Check, p.Provider
		facts, err := runPreflight(ctx, dependencies, policy, check, provider, p.Project, p.Preflight, opts.Prebuilt, env)
		check.End(err)
		if err != nil {
			return err
		}
		if facts.declined {
			run.Succeed("Nothing deployed to " + place(env))
			return nil
		}
		cfg := facts.project
		infra := newInfraProvisioning(provider, env, facts)
		defer func() { err = errors.Join(err, infra.release(ctx, cfg.Slug)) }()
		if !opts.Dry {
			attempt = p.NewAttempt(ctx, target.kind(), cfg, env, dependencies.DiscoverPRNumber())
		}

		d := newDeployRun(dependencies, target, cfg, provider, env, facts, infra, opts, dependencies.IsBrowserReachable(stdin))
		phase := run.Phase(progressv1.Phase_PHASE_BUILD)
		releaseOutput, err := holdBuildOutput(ctx, cfg, phase)
		if err != nil {
			phase.End(err)
			return err
		}
		defer releaseOutput()
		manifest, inline, err := d.buildManifest(ctx, phase)
		phase.End(err)
		if err != nil {
			return err
		}
		deployTelemetry.noteManifest(manifest)
		if manifest == nil {
			run.Succeed(nothingToDeploy(cfg))
			return nil
		}
		apps, _ = appsDeployed(cfg, manifest, nil, env)

		req, err := d.request(manifest, inline)
		if err != nil {
			return err
		}

		if opts.Dry {
			return showDeployPlan(ctx, run, provider, req, "Proposed changes to "+place(env), cfg.Slug, place(env), describePlannedPreBuild(d.preBuild, opts.Prebuilt))
		}

		infra.stopRenewing()
		out, err := streamDeploy(ctx, provider, req)
		var unread error
		apps, unread = appsDeployed(cfg, manifest, out.apps, env)
		if err != nil {
			return err
		}
		infra.markShipped()
		deployTelemetry.noteDeployed()

		if succeeded, err = deployreport.WriteSucceeded(attempt, apps, out.promotion(target.tag), unread); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Deployed %s to %s", cfg.Slug, place(env)))
		return nil
	})
	deployTelemetry.record(dependencies.RecordEvent, err)
	dependencies.Console.ReportAttempt(ctx, attempt, apps, succeeded, err, stderr)
	return err
}

func toCloud(ctx context.Context, dependencies Dependencies, resolved commands.Target, target Target, opts Options) error {
	switch {
	case opts.Dry:
		return commands.NotOnCloud(target.command() + " --dry")
	case opts.Prebuilt:
		return commands.NotOnCloud(target.command() + " --prebuilt")
	}
	return dependencies.UploadToCloud(ctx, resolved, target.command(), target.tier)
}
