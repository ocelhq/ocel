package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
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
	forwards, records, err := forwardDeployed(ctx, opts, deployed)
	deployed.Check.End(err)
	if err != nil {
		return err
	}
	live := map[string]string{}
	maps.Copy(live, records)
	maps.Copy(live, forwards.Bindings(portforward.WholeProject))
	hidden := redaction.NewValues(build.SecretValues(live))
	stdout, stderr := hidden.Writer(opts.Stdout), hidden.Writer(opts.Stderr)
	if opts.Stdout == opts.Stderr {
		stderr = stdout
	}
	opts.Stdout, opts.Stderr = stdout, stderr
	ran := RunProjected(ctx, opts, live)
	return errors.Join(ran, stdout.Flush(), stderr.Flush(), closeForwards(opts, forwards))
}

func closeForwards(opts Options, forwards *portforward.Forwards) error {
	if forwards == nil {
		return nil
	}
	span := opts.session().Child(opts.Project.Slug, closingForwardsTitle)
	err := forwards.Close()
	span.End(err)
	return err
}

func forwardDeployed(ctx context.Context, opts Options, deployed Deployed) (*portforward.Forwards, map[string]string, error) {
	cfg, check, env := opts.Project, deployed.Check, deployed.Environment
	declarations, err := readDeclarations(ctx, cfg, deployed)
	if err != nil {
		return nil, nil, err
	}
	resources, err := collectResources(ctx, opts, deployed, declarations)
	if err != nil {
		return nil, nil, err
	}
	infra, err := manifest.AssembleInfra(manifest.InfraInput{
		Project:        cfg,
		Tier:           env.GetTier(),
		Resources:      resources,
		WorkerCeilings: provider.WorkerCeilingsOf(deployed.Provider.Facts().GetWorkerCeilings()),
	})
	if err != nil {
		return nil, nil, err
	}
	inline, err := inlinebinding.Read(ctx, cfg, declarations)
	if err != nil {
		return nil, nil, err
	}
	read, err := portforward.ReadRecords(portforward.ListWholeProjectRecords(infra), inline)
	if err != nil {
		return nil, nil, err
	}
	records := read[portforward.WholeProject]
	uses := portforward.ListWholeProjectUses(infra)
	if len(uses) == 0 {
		if len(records) == 0 {
			check.Say(cfg.Slug + " declares no resource a port can reach, so the command runs without bindings")
		}
		return nil, records, nil
	}
	if !deployed.Provider.Facts().GetForwardsPorts() {
		return nil, nil, fmt.Errorf("the provider of %s forwards no port to a deployed environment, so a command cannot reach its resources from this machine", cfg.Slug)
	}
	declared := portforward.ListDeclared(uses)
	step := check.Child(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)))
	forwards, err := portforward.Open(ctx, deployed.Provider, cfg.Slug, env, uses, check)
	if err != nil {
		err = portforward.RefuseUnforwarded("the command", declared, err)
	}
	step.End(err)
	if err != nil {
		return nil, nil, err
	}
	if unforwarded := forwards.Unforwarded(); len(unforwarded) > 0 {
		check.Warn(portforward.DescribeUnforwarded("The command", unforwarded))
	}
	return forwards, records, nil
}

func readDeclarations(ctx context.Context, cfg *project.Project, deployed Deployed) (*variables.Declarations, error) {
	tier := deployed.Environment.GetTier()
	store := variables.Store{Provider: deployed.Provider, Project: cfg, Tier: tier}
	scope := variables.ScopeOf(cfg, tier, deployed.Environment.GetIdentity())
	described, err := store.DescribeEnvSource(ctx)
	if err != nil {
		return nil, err
	}
	scope.EnvSource = described
	return variables.NewDeclarations(store, scope), nil
}

func collectResources(ctx context.Context, opts Options, deployed Deployed, declarations *variables.Declarations) ([]declaration.Resource, error) {
	cfg := opts.Project
	collecting := opts.Run.Phase(progressv1.Phase_PHASE_BUILD).Child(cfg.Slug, progress.Collecting.Title("the resources "+cfg.Slug+" declares"))
	said := collecting.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	resources, err := deployed.CollectDeclarations(ctx, cfg, declarations, said, said)
	collecting.End(err)
	return resources, err
}
