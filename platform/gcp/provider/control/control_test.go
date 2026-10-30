package control_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func refusedAsNeedingARunner(t *testing.T, call string, err error) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNeedsRunner {
		t.Errorf("%s = %v, want a %s refusal: this assembly runs no provisioning engine, and the client re-issues the call as a job on a runner", call, err, refusal.CodeNeedsRunner)
	}
}

func TestTheControlAssemblyRefusesEveryEngineCallAsNeedingARunner(t *testing.T) {
	t.Parallel()

	p := &control.Provider{Options: control.Options{Project: "acme-prod", Region: "europe-west1"}}
	ctx := context.Background()
	spec := provider.StackSpec{Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction}}

	_, err := p.Stacks().Plan(ctx, spec, progress.Discard())
	refusedAsNeedingARunner(t, "Stacks().Plan()", err)
	_, err = p.Stacks().Provision(ctx, spec, progress.Discard())
	refusedAsNeedingARunner(t, "Stacks().Provision()", err)
	refusedAsNeedingARunner(t, "Stacks().Destroy()", p.Stacks().Destroy(ctx, provider.StackDestroy{Ref: spec.Ref}, progress.Discard()))
	_, err = p.Runtime().FunctionBase(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNext})
	refusedAsNeedingARunner(t, "Runtime().FunctionBase()", err)
	_, err = p.Bootstrap(edge.None)
	refusedAsNeedingARunner(t, "Bootstrap()", err)
	_, err = p.Connector().Install(ctx, provider.ConnectorInstall{}, progress.Discard())
	refusedAsNeedingARunner(t, "Connector().Install()", err)
	refusedAsNeedingARunner(t, "Connector().Remove()", p.Connector().Remove(ctx, progress.Discard()))
}

func TestTheControlAssemblyRoutesThroughTheALBEdge(t *testing.T) {
	t.Parallel()

	p := &control.Provider{Options: control.Options{Project: "acme-prod", Region: "europe-west1"}}
	if front, err := p.Edges().Open(albresources.Kind, nil); err != nil || front.Kind() != albresources.Kind {
		t.Errorf("Edges().Open(alb) = %v, %v, want the alb edge: rollbacks and url map routes on its projects need no engine", front, err)
	}
	if routes, err := p.Routers().Open(router.Kind(albresources.Kind)); err != nil || routes.Kind() != router.Kind(albresources.Kind) {
		t.Errorf("Routers().Open(alb) = %v, %v, want the alb router", routes, err)
	}
}

func TestTheControlAssemblyRefusesToAddAnyBootstrapPartAsNeedingARunner(t *testing.T) {
	p, err := control.NewProvider(control.Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}
	ctx := context.Background()
	refusedAsNeedingARunner(t, "Engine.EnsureTierKeys()", p.Engine.EnsureTierKeys(ctx, environment.TierProduction, "shop"))
	refusedAsNeedingARunner(t, "Engine.EnsureProjectDatabaseRecorded()", p.Engine.EnsureProjectDatabaseRecorded(ctx, environment.TierProduction, "shop"))
}

func TestTheControlAssemblyRefusesEveryHookThatProvisionsAsNeedingARunner(t *testing.T) {
	t.Parallel()

	hooks := (&control.Provider{}).Hooks()
	ctx := context.Background()
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "shop"}}

	_, err := hooks.Preflight.Plan(ctx, pre)
	refusedAsNeedingARunner(t, "Preflight.Plan()", err)
	refusedAsNeedingARunner(t, "Preflight.Apply()", hooks.Preflight.Apply(ctx, pre))
	_, err = hooks.InfraPreflight.Plan(ctx, pre)
	refusedAsNeedingARunner(t, "InfraPreflight.Plan()", err)
	refusedAsNeedingARunner(t, "InfraPreflight.Apply()", hooks.InfraPreflight.Apply(ctx, pre))
	_, err = hooks.ProgramEdge(ctx, provider.EdgeProgramRequest{})
	refusedAsNeedingARunner(t, "ProgramEdge()", err)
	_, err = hooks.EnsureImageRegistry(ctx, environment.TierProduction)
	refusedAsNeedingARunner(t, "EnsureImageRegistry()", err)
	_, err = hooks.OpenDirectImages(ctx)
	refusedAsNeedingARunner(t, "OpenDirectImages()", err)
	_, err = hooks.CheckBucket(ctx, nil, false, nil)
	refusedAsNeedingARunner(t, "CheckBucket()", err)
	refusedAsNeedingARunner(t, "EnvSource.EnsureSync()", hooks.EnvSource.EnsureSync(ctx, environment.TierProduction, "shop"))
	_, err = hooks.BindingAccess.ForwardPorts(ctx, provider.PortForwardRequest{}, progress.Discard())
	refusedAsNeedingARunner(t, "BindingAccess.ForwardPorts()", err)
	_, err = hooks.BindingAccess.ServeProxy(ctx, provider.BindingProxyRequest{}, progress.Discard())
	refusedAsNeedingARunner(t, "BindingAccess.ServeProxy()", err)
	_, err = hooks.Cost.Inventory(ctx, provider.InventoryRequest{})
	refusedAsNeedingARunner(t, "Cost.Inventory()", err)
	_, err = hooks.Retirement.Plan(ctx, provider.LiveApps{}, progress.Discard())
	refusedAsNeedingARunner(t, "Retirement.Plan()", err)
	refusedAsNeedingARunner(t, "Retirement.Retire()", hooks.Retirement.Retire(ctx, provider.LiveApps{}, progress.Discard()))
	_, err = hooks.ProjectRemoval.Plan(ctx, environment.TierProduction, "shop")
	refusedAsNeedingARunner(t, "ProjectRemoval.Plan()", err)
	refusedAsNeedingARunner(t, "ProjectRemoval.Remove()", hooks.ProjectRemoval.Remove(ctx, environment.TierProduction, "shop", progress.Discard()))
}
