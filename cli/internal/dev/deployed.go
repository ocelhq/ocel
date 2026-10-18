package dev

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/portforward"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Deployed struct {
	Provider            *providerprocess.Provider
	Check               *run.Span
	Environment         *environmentv1.Environment
	CollectDeclarations func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
}

func RunDeployed(ctx context.Context, opts Options, deployed Deployed) error {
	forwards, err := forwardDeployed(ctx, opts, deployed)
	deployed.Check.End(err)
	if err != nil {
		return err
	}
	live := forwards.Bindings(portforward.WholeProject)
	hidden := redaction.NewValues(build.SecretValues(live))
	stdout, stderr := hidden.Writer(opts.Stdout), hidden.Writer(opts.Stderr)
	if opts.Stdout == opts.Stderr {
		stderr = stdout
	}
	opts.Stdout, opts.Stderr = stdout, stderr
	ran := RunProjected(ctx, opts, live)
	return errors.Join(ran, stdout.Flush(), stderr.Flush(), forwards.Close())
}

func forwardDeployed(ctx context.Context, opts Options, deployed Deployed) (*portforward.Forwards, error) {
	cfg, check, env := opts.Project, deployed.Check, deployed.Environment
	if !deployed.Provider.Facts().GetForwardsPorts() {
		return nil, fmt.Errorf("the provider of %s forwards no port to a deployed environment, so a command cannot reach its resources from this machine", cfg.Slug)
	}
	resources, err := collectResources(ctx, opts, deployed)
	if err != nil {
		return nil, err
	}
	infra, err := manifest.AssembleInfra(manifest.InfraInput{
		Project:        cfg,
		Tier:           env.GetTier(),
		Resources:      resources,
		WorkerCeilings: provider.WorkerCeilingsOf(deployed.Provider.Facts().GetWorkerCeilings()),
	})
	if err != nil {
		return nil, err
	}
	uses := portforward.ListWholeProjectUses(infra)
	if len(uses) == 0 {
		check.Say(cfg.Slug + " declares no resource a port can reach, so the command runs without bindings")
		return nil, nil
	}
	declared := portforward.ListDeclared(uses)
	step := check.Child(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)))
	forwards, err := portforward.Open(ctx, deployed.Provider, cfg.Slug, env, uses, check)
	if err != nil {
		err = portforward.RefuseUnforwarded("the command", declared, err)
	}
	step.End(err)
	if err != nil {
		return nil, err
	}
	if unforwarded := forwards.Unforwarded(); len(unforwarded) > 0 {
		check.Warn(portforward.DescribeUnforwarded("The command", unforwarded))
	}
	return forwards, nil
}

func collectResources(ctx context.Context, opts Options, deployed Deployed) ([]declaration.Resource, error) {
	cfg := opts.Project
	collecting := opts.Run.Phase(progressv1.Phase_PHASE_BUILD).Child(cfg.Slug, progress.Collecting.Title("the resources "+cfg.Slug+" declares"))
	said := collecting.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	declarations := variables.NewDeclarations(variables.NoValues{}, variables.Scope{Apps: variables.AppsOf(cfg)})
	resources, err := deployed.CollectDeclarations(ctx, cfg, declarations, said, said)
	collecting.End(err)
	return resources, err
}
