package commands

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type OpenOptions struct {
	Pinning executables.Pinning
	Tier    environmentv1.Tier
	Require readiness.Requirement
	Slug    string
	Domains []string
}

func (i Invocation) OpenProvider(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, readiness.Preflight, error) {
	provider, err := providerprocess.Start(ctx, cfg, check, i.Questions, opts.Pinning)
	if err != nil {
		return nil, readiness.Preflight{}, err
	}
	if opts.Require == readiness.None {
		return provider, readiness.Preflight{}, nil
	}
	read, err := readiness.Check(ctx, check, provider, cfg, readiness.Request{
		Tier:    opts.Tier,
		Require: opts.Require,
		Slug:    opts.Slug,
		Domains: opts.Domains,
	})
	if err != nil {
		provider.Close()
		return nil, readiness.Preflight{}, err
	}
	return provider, read, nil
}

type ProviderRun struct {
	Run       *run.Run
	Check     *run.Span
	Provider  *providerprocess.Provider
	Preflight readiness.Preflight
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
	provider, preflight, err := i.OpenProvider(ctx, check, cfg, opts)
	if err != nil {
		check.End(err)
		return err
	}
	defer provider.Close()
	err = work(ctx, ProviderRun{Run: running, Check: check, Provider: provider, Preflight: preflight})
	check.End(err)
	return err
}
