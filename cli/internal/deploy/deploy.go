package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
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
	NoWait   bool
}

func To(ctx context.Context, dependencies Dependencies, cwd string, target Target, opts Options, stdout, stderr io.Writer, stdin io.Reader) error {
	policy, resolved, err := ensureTarget(ctx, dependencies, target.command(), cwd, opts.Yes, opts.Dry, stdout, stdin)
	if prerequisite.IsDeclined(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if resolved.IsCloud() {
		return toCloud(ctx, dependencies, cwd, resolved, target, opts, stderr, stdin)
	}
	if opts.NoWait {
		return commands.RefuseNoWait()
	}
	if opts.Yes && dependencies.JobAnswers != nil {
		return &clierror.Error{Code: clierror.CodeUsage, Cause: fmt.Errorf("`%s` runs inside an Ocel Cloud job, where the console answers every confirmation, so --yes would consent for a person nobody asked: drop it", target.command())}
	}
	cfg := resolved.Project
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
	var report *consolev1.Deployment
	err = dependencies.WithProvider(ctx, cfg, target.command(), commands.OpenOptions{Intent: commands.Deploy, Tier: target.tier, Policy: policy, DryRun: policy.DryRun}, func(ctx context.Context, p commands.ProviderRun) (err error) {
		run, check, provider := p.Run, p.Check, p.Provider
		var attempt *deployreport.Attempt
		var apps []*consolev1.App
		var succeeded *consolev1.Deployment
		defer func() {
			report = attempt.Report(time.Now(), apps, succeeded, err)
			run.SetDeploymentReport(report)
		}()
		facts, err := runPreflight(ctx, dependencies, policy, check, provider, p.Project, p.Preflight, opts.Prebuilt, env)
		if err == nil && !facts.declined && !opts.Dry {
			attempt = p.NewAttempt(ctx, check, target.kind(), facts.project, env, dependencies.DiscoverPRNumber())
		}
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
		defer func() { err = errors.Join(err, infra.release(ctx, run, cfg.Slug)) }()

		d := newDeployRun(dependencies, target, cfg, provider, env, facts, infra, policy, opts, dependencies.IsBrowserReachable(stdin))
		phase := run.Phase(progressv1.Phase_PHASE_BUILD)
		releaseOutput, err := build.LockOutput(ctx, phase, cfg)
		if err != nil {
			phase.End(err)
			return err
		}
		defer releaseOutput()
		manifest, inline, err := d.buildManifest(ctx, phase)
		phase.End(err)
		if prerequisite.IsDeclined(err) {
			run.Succeed(err.Error())
			return nil
		}
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
	dependencies.Console.ReportDeployment(ctx, cfg.Dir, report, stderr)
	return err
}

func toCloud(ctx context.Context, dependencies Dependencies, cwd string, resolved commands.Target, target Target, opts Options, stderr io.Writer, stdin io.Reader) error {
	switch {
	case opts.Dry:
		return commands.NotOnCloud(target.command() + " --dry")
	case opts.Prebuilt:
		return commands.NotOnCloud(target.command() + " --prebuilt")
	case opts.Yes:
		return &clierror.Error{Code: clierror.CodeCloudUnsupported, Cause: fmt.Errorf("`%s --yes` does not run on Ocel Cloud: a job asks each confirmation on its page in the console, so --yes has nothing here to consent to — drop it, and answer there", target.command())}
	}
	job := commands.CloudJob{Command: target.command(), Tier: target.tier, Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY, Tag: target.tag, NoWait: opts.NoWait, Stderr: stderr}
	if target.isPreview() {
		env, err := target.environment(dependencies, cwd)
		if err != nil {
			return err
		}
		job.Kind = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP
		job.Preview = &cloudv1.PreviewTarget{Name: env.GetIdentity(), Label: env.GetLabel(), Persistent: target.persistent}
	}
	if dependencies.IsBrowserReachable(stdin) {
		job.OpenBrowser = dependencies.OpenBrowser
	}
	return dependencies.RunOnCloud(ctx, resolved, job)
}
