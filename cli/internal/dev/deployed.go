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
	forwards, live, err := reachDeployed(ctx, opts, deployed)
	deployed.Check.End(err)
	if err != nil {
		return err
	}
	hidden := redaction.NewValues(build.SecretValues(live))
	hidden.HideInTrace(ctx)
	stdout, stderr := hidden.Writer(opts.Stdout), hidden.Writer(opts.Stderr)
	if opts.Stdout == opts.Stderr {
		stderr = stdout
	}
	opts.Stdout, opts.Stderr = stdout, stderr
	ran := RunProjected(ctx, opts, live)
	return errors.Join(ran, stdout.Flush(), stderr.Flush(), closeForwards(ctx, opts, forwards))
}

func closeForwards(ctx context.Context, opts Options, forwards *portforward.Forwards) error {
	if forwards == nil {
		return nil
	}
	_, span := opts.session().Child(ctx, opts.Project.Slug, closingForwardsTitle)
	err := forwards.Close()
	span.End(err)
	return err
}

func reachDeployed(ctx context.Context, opts Options, deployed Deployed) (*portforward.Forwards, map[string]string, error) {
	reach, records, err := readReach(ctx, opts, deployed)
	if err != nil {
		return nil, nil, err
	}
	if len(reach.Forwarded) == 0 && len(records) == 0 {
		deployed.Check.Say(opts.Project.Slug + " declares no resource a port can reach, so the command runs without bindings")
	}
	forwards, err := forwardDeployed(ctx, opts, deployed, reach.Forwarded)
	if err != nil {
		return nil, nil, err
	}
	live := map[string]string{}
	maps.Copy(live, records)
	maps.Copy(live, forwards.Bindings(portforward.WholeProject))
	return forwards, live, nil
}

func readReach(ctx context.Context, opts Options, deployed Deployed) (portforward.Reach, map[string]string, error) {
	cfg := opts.Project
	declarations, err := variables.ReadDeployedDeclarations(ctx, deployed.Provider, cfg, deployed.Environment)
	if err != nil {
		return portforward.Reach{}, nil, err
	}
	resources, err := collectResources(ctx, opts, deployed, declarations)
	if err != nil {
		return portforward.Reach{}, nil, err
	}
	infra, err := manifest.AssembleInfra(manifest.InfraInput{
		Project:        cfg,
		Tier:           deployed.Environment.GetTier(),
		Resources:      resources,
		WorkerCeilings: provider.WorkerCeilingsOf(deployed.Provider.Facts().GetWorkerCeilings()),
	})
	if err != nil {
		return portforward.Reach{}, nil, err
	}
	reach := portforward.ReachWholeProject(infra)
	inline, err := inlinebinding.Read(ctx, cfg, declarations)
	if err != nil {
		return portforward.Reach{}, nil, err
	}
	records, err := portforward.LiveBindings(reach.Recorded, inline)
	return reach, records[portforward.WholeProject], err
}

func forwardDeployed(ctx context.Context, opts Options, deployed Deployed, uses []portforward.Use) (*portforward.Forwards, error) {
	cfg, check := opts.Project, deployed.Check
	if len(uses) == 0 {
		return nil, nil
	}
	if !deployed.Provider.Facts().GetForwardsPorts() {
		return nil, fmt.Errorf("the provider of %s forwards no port to a deployed environment, so a command cannot reach its resources from this machine", cfg.Slug)
	}
	declared := portforward.ListDeclared(uses)
	stepCtx, step := check.Child(ctx, cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)))
	forwards, err := portforward.Open(stepCtx, deployed.Provider, cfg.Slug, deployed.Environment, uses, check)
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

func collectResources(ctx context.Context, opts Options, deployed Deployed, declarations *variables.Declarations) ([]declaration.Resource, error) {
	cfg := opts.Project
	collectingCtx, collecting := opts.Run.Phase(progressv1.Phase_PHASE_BUILD).Child(ctx, cfg.Slug, progress.Collecting.Title("the resources "+cfg.Slug+" declares"))
	said := collecting.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED)
	resources, err := deployed.CollectDeclarations(collectingCtx, cfg, declarations, said, said)
	collecting.End(err)
	return resources, err
}
