package commands

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/endpointcall"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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
	Cloud       *CloudTarget
	CloudScopes []scopev1.Scope

	ConsoleCredential bool
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

func (o OpenOptions) sessionScopes() []scopev1.Scope {
	scopes := append([]scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ}, o.CloudScopes...)
	slices.Sort(scopes)
	return slices.Compact(scopes)
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
	provider, read, err := i.openChecked(ctx, check, cfg, opts)
	if err != nil && provider != nil {
		provider.Close()
		return nil, readiness.Preflight{}, err
	}
	return provider, read, err
}

func (i Invocation) openChecked(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, readiness.Preflight, error) {
	ctx = run.ContextWithSpan(ctx, check)
	req := opts.request(cfg)
	if opts.Intent.deploys() {
		var err error
		if req.Registry, err = readiness.ProjectRegistry(cfg); err != nil {
			return nil, readiness.Preflight{}, err
		}
	}
	provider, identity, account, err := i.start(ctx, check, cfg, opts)
	if err != nil {
		return nil, readiness.Preflight{}, err
	}
	if req.Require == readiness.None {
		return provider, readiness.Preflight{}, nil
	}
	req.Identity, req.Account = identity, account
	read, err := readiness.Check(ctx, check, provider, cfg, req)
	return provider, read, err
}

func (i Invocation) start(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, *contractv1.Identity, string, error) {
	if opts.Cloud == nil {
		provider, account, err := i.startWithConsoleCredential(ctx, check, cfg, opts)
		return provider, nil, account, err
	}
	scopes := opts.sessionScopes()
	if open := i.CloudSessions.find(check.Run(), cfg, scopes); open != nil {
		return open.provider, open.identity, "", nil
	}
	openingCtx, opening := check.Child(ctx, CloudProviderID, progress.Title{Started: "Opening a session on Ocel Cloud", Ended: "Opened a session on Ocel Cloud"})
	open, err := i.openOnCloud(openingCtx, check, cfg, opts.Cloud, scopes)
	opening.End(err)
	if err != nil {
		return nil, nil, "", err
	}
	i.CloudSessions.keep(check.Run(), cfg, open)
	return open.provider, open.identity, "", nil
}

func (i Invocation) openOnCloud(ctx context.Context, check *run.Span, cfg *project.Project, cloud *CloudTarget, scopes []scopev1.Scope) (*openCloudSession, error) {
	if cloud.Offer.ProviderURL == "" {
		return nil, &clierror.Error{Code: clierror.CodeCloudUnsupported, Cause: fmt.Errorf("%s offers Ocel Cloud and names no provider URL for it, so only `ocel deploy`, `ocel preview up`, `ocel preview rm`, `ocel projects destroy` and `ocel build` reach it, as jobs: name a provider in this project's config to run this against your own account", cloud.ConsoleURL)}
	}
	if served := cloud.Offer.Version; served != "" && served != version.Version {
		return nil, endpointcall.RefuseUnserved(cloudVendor, cloud.Offer.ProviderURL, []string{served})
	}
	account := NewConsoleAccount(cloud.ConsoleURL, cloud.Credentials.AccessToken)
	org, err := pickOrganization(ctx, account, cfg.Dir)
	if err != nil {
		return nil, err
	}
	tokens := console.NewEndpointTokens(account.Client, account.Session, org.ID, console.CloudEndpointID, scopes...)
	provider, err := providerprocess.StartRemote(ctx, cfg, check, i.Questions, providerprocess.Remote{Name: CloudProviderID, URL: cloud.Offer.ProviderURL, Tokens: tokens})
	if err != nil {
		return nil, err
	}
	return &openCloudSession{provider: provider, identity: consoleIdentity(org, cloudVendor, cloud.Credentials.Email, cloud.ConsoleURL), scopes: scopes}, nil
}

type ProviderRun struct {
	Run       *run.Run
	Check     *run.Span
	Provider  *providerprocess.Provider
	Preflight readiness.Preflight
	Project   *project.Project

	job   *jobcontext.Job
	cloud *CloudTarget
}

func (i Invocation) WithProvider(ctx context.Context, cfg *project.Project, command string, opts OpenOptions, work func(context.Context, ProviderRun) error) (err error) {
	if opts.Cloud == nil {
		if _, err := i.RequireProvider(ctx, cfg, command); err != nil {
			return err
		}
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
			readingCtx, reading := check.Child(ctx, cfg.Slug, progress.Reading.Title("the project's config again"))
			provider.Close()
			provider = nil
			reloaded, err := i.ResolveTarget(readingCtx, cfg.Dir)
			reading.End(err)
			if err != nil {
				return err
			}
			loaded = reloaded.Project
		}
		provider, preflight, err = i.openChecked(ctx, check, loaded, opts)
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
	err = work(ctx, ProviderRun{Run: running, Check: check, Provider: provider, Preflight: preflight, Project: loaded, job: i.Job(), cloud: opts.Cloud})
	check.End(err)
	return err
}

func providerID(cfg *project.Project) string {
	if cfg.Provider == nil {
		return ""
	}
	return cfg.Provider.ID
}
