package commands

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	providercontract "github.com/ocelhq/ocel/pkg/provider"
)

type Intent int

const (
	Launch Intent = iota
	UseAccount
	ManageBootstrap
	UseInfrastructure
	UseVariablesKey
	UseParts
	Deploy
)

type OpenOptions struct {
	Intent      Intent
	Tier        environmentv1.Tier
	Policy      consent.Policy
	DryRun      bool
	RunsOnCloud bool
}

func (i Intent) requirement() readiness.Requirement {
	switch i {
	case UseAccount, ManageBootstrap:
		return readiness.Credentials
	case UseInfrastructure, UseVariablesKey:
		return readiness.Infrastructure
	case UseParts, Deploy:
		return readiness.Parts
	}
	return readiness.None
}

func (i Intent) deploys() bool {
	return i == Deploy
}

func (o OpenOptions) request(cfg *project.Project) readiness.Request {
	req := readiness.Request{Tier: o.Tier, Require: o.Intent.requirement(), DryRun: o.DryRun}
	switch o.Intent {
	case ManageBootstrap:
		req.Slug = cfg.Slug
	case UseVariablesKey:
		req.Slug, req.Part, req.RecordWants = cfg.Slug, providercontract.VariablesKeyPart, true
	case Deploy:
		req.Slug, req.RecordWants, req.Domains = cfg.Slug, true, cfg.HostnameNames(req.Tier)
		req.RequireHostname = req.Tier == environmentv1.Tier_TIER_PRODUCTION
		req.CheckPreviewRoute = req.Tier == environmentv1.Tier_TIER_PREVIEW
		req.CheckForwarding = true
	}
	return req
}

func (i Invocation) OpenProvider(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, readiness.Preflight, error) {
	var cloud *CloudTarget
	if opts.RunsOnCloud {
		var err error
		if cloud, err = i.ResolveProvider(ctx, cfg, "", opts); err != nil {
			return nil, readiness.Preflight{}, err
		}
	}
	provider, read, err := i.openChecked(ctx, check, cfg, opts, cloud)
	if err != nil && provider != nil {
		provider.Close()
		return nil, readiness.Preflight{}, err
	}
	return provider, read, err
}

func (i Invocation) openChecked(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions, cloud *CloudTarget) (*providerprocess.Provider, readiness.Preflight, error) {
	req := opts.request(cfg)
	if opts.Intent.deploys() {
		var err error
		if req.Registry, err = readiness.ProjectRegistry(cfg); err != nil {
			return nil, readiness.Preflight{}, err
		}
	}
	provider, err := i.start(ctx, check, cfg, opts, cloud)
	if err != nil {
		return nil, readiness.Preflight{}, err
	}
	if req.Require == readiness.None {
		return provider, readiness.Preflight{}, nil
	}
	read, err := readiness.Check(ctx, check, provider, cfg, req)
	return provider, read, err
}

func (i Invocation) start(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions, cloud *CloudTarget) (*providerprocess.Provider, error) {
	if cloud == nil {
		loading := check.Child(providerID(cfg), progress.Loading.Title("the provider"))
		provider, err := providerprocess.Start(ctx, cfg, check, i.Questions, executables.ChoosePinning(opts.DryRun))
		loading.End(err)
		return provider, err
	}
	opening := check.Child(CloudProviderID, progress.Title{Started: "Opening a session on Ocel Cloud", Ended: "Opened a session on Ocel Cloud"})
	provider, err := i.startOnCloud(ctx, check, cfg, cloud)
	opening.End(err)
	return provider, err
}

func (i Invocation) startOnCloud(ctx context.Context, check *run.Span, cfg *project.Project, cloud *CloudTarget) (*providerprocess.Provider, error) {
	if cloud.Offer.ProviderURL == "" {
		return nil, &clierror.Error{Code: clierror.CodeCloudUnsupported, Cause: fmt.Errorf("%s offers Ocel Cloud and names no provider URL for it, so only `ocel deploy`, `ocel preview up` and `ocel build` reach it, as jobs: name a provider in this project's config to run this against your own account", cloud.ConsoleURL)}
	}
	tokens := console.NewCloudTokens(console.New(cloud.ConsoleURL), cloud.Credentials.AccessToken)
	return providerprocess.StartRemote(ctx, cfg, check, i.Questions, providerprocess.Remote{Name: CloudProviderID, URL: cloud.Offer.ProviderURL, Tokens: tokens})
}

type ProviderRun struct {
	Run       *run.Run
	Check     *run.Span
	Provider  *providerprocess.Provider
	Preflight readiness.Preflight
	Project   *project.Project

	job *BuilderJob
}

func (i Invocation) WithProvider(ctx context.Context, cfg *project.Project, command string, opts OpenOptions, work func(context.Context, ProviderRun) error) (err error) {
	cloud, err := i.ResolveProvider(ctx, cfg, command, opts)
	if err != nil {
		return err
	}
	ctx, running, err := i.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	var provider *providerprocess.Provider
	defer func() {
		if provider != nil {
			provider.Close()
		}
	}()
	var preflight readiness.Preflight
	loaded := cfg
	err = i.Setups.Ensure(ctx, opts.Policy, check, func(ctx context.Context) (err error) {
		if provider != nil {
			provider.Close()
			provider = nil
			if loaded, err = i.reloadProject(ctx, cfg, cloud); err != nil {
				return err
			}
		}
		provider, preflight, err = i.openChecked(ctx, check, loaded, opts, cloud)
		return err
	})
	if prerequisite.IsDeclined(err) {
		check.End(nil)
		running.Succeed(err.Error())
		return nil
	}
	if err != nil {
		check.End(err)
		return err
	}
	err = work(ctx, ProviderRun{Run: running, Check: check, Provider: provider, Preflight: preflight, Project: loaded, job: i.BuilderJob})
	check.End(err)
	return err
}

func (i Invocation) reloadProject(ctx context.Context, cfg *project.Project, cloud *CloudTarget) (*project.Project, error) {
	if cloud != nil {
		return i.LoadTargetProject(ctx, cfg.Dir)
	}
	if i.BuilderJob != nil {
		return i.BuilderJob.loadProject(ctx, cfg.Dir, i.ConfigPath())
	}
	return project.Load(ctx, cfg.Dir, cfg.Path)
}

func providerID(cfg *project.Project) string {
	if cfg.Provider == nil {
		return ""
	}
	return cfg.Provider.ID
}
