package commands

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type OpenOptions struct {
	Pinning         executables.Pinning
	Tier            environmentv1.Tier
	Require         readiness.Requirement
	Slug            string
	ClaimsDomains   bool
	RequireHostname bool
	Feature         string
	Policy          consent.Policy
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
	provider, err := providerprocess.Start(ctx, cfg, check, i.Questions, opts.Pinning)
	if err != nil {
		return nil, readiness.Preflight{}, err
	}
	if opts.Require == readiness.None {
		return provider, readiness.Preflight{}, nil
	}
	req := readiness.Request{
		Tier:            opts.Tier,
		Require:         opts.Require,
		Slug:            opts.Slug,
		RequireHostname: opts.RequireHostname,
		Feature:         opts.Feature,
	}
	if opts.ClaimsDomains {
		req.Domains = cfg.HostnameNames(opts.Tier)
	}
	read, err := readiness.Check(ctx, check, provider, cfg, req)
	return provider, read, err
}

type ProviderRun struct {
	Run       *run.Run
	Check     *run.Span
	Provider  *providerprocess.Provider
	Preflight readiness.Preflight
	Project   *project.Project
}

func (i Invocation) WithProvider(ctx context.Context, cfg *project.Project, command string, opts OpenOptions, work func(context.Context, ProviderRun) error) (err error) {
	if _, err := cfg.RequireProvider(); err != nil {
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
			if loaded, err = project.Load(ctx, cfg.Dir, cfg.Path); err != nil {
				return err
			}
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
	err = work(ctx, ProviderRun{Run: running, Check: check, Provider: provider, Preflight: preflight, Project: loaded})
	check.End(err)
	return err
}
