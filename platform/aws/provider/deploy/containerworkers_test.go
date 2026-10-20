package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

var mediaOnContainers = provider.WorkerSpec{Name: "media", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func containerStacksHostingWorkers(t *testing.T, store records.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = store
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	cfg.StateTable, cfg.StateTableARN = "ocel-state", testStateTableARN
	cfg.Parameters, cfg.KVTokenRoot = newFakeParameters(), conformanceKVTokenRoot
	cfg.SigningKeys, cfg.SigningKeyRoot = newFakeSigningKeys(), conformanceSigningKeyRoot
	spec.Ref.Name = naming.AppStack("prod", "web", fixedRelease(t))
	spec.App.Workers = []provider.WorkerSpec{mediaOnContainers}
	spec.App.DeploymentWorkers = spec.App.Workers
	spec.App.Topics = []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "media"})}
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		container.OutputKeyURL:      "http://" + fixtureOrigin,
		container.OutputKeyPhysical: containerPhysical,
	}}
	outputs[resources.WorkerName("media")] = auto.OutputValue{Value: map[string]any{container.OutputKeyPhysical: container.WorkerServiceName("shop", "prod", "web", "media")}}
	engine := &mockedEngine{outputs: outputs}
	return stacksWith(cfg, engine), engine, spec
}

func TestAContainerAppHostingWorkersProvisionsTheirStackBesideItsOwnAndReturnsBoth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)

	result, err := stacks.Provision(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	var returned []string
	for _, container := range result.Containers {
		returned = append(returned, container.Name)
	}
	if !slices.Contains(returned, "web") || !slices.Contains(returned, resources.WorkerName("media")) || len(returned) != 2 {
		t.Errorf("Containers = %v, want the app and its media worker", returned)
	}
	if !slices.Contains(engine.stacks(), naming.ContainerWorkersStack("prod", "web").String()) {
		t.Errorf("stacks run = %v, want the workers stack %s among them", engine.stacks(), naming.ContainerWorkersStack("prod", "web"))
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, consumer := range consumers {
		held = append(held, consumer.Key.String())
	}
	if !slices.ContainsFunc(held, func(key string) bool {
		return strings.Contains(key, naming.ContainerWorkersStack("prod", "web").String())
	}) {
		t.Errorf("container infrastructure consumers = %v, want the workers stack among them: the cluster must outlive the services in it", held)
	}
}

func TestTakingDownAnEnvironmentTakesItsContainerWorkersAndLetsGoTheirHoldOnTheClusterInfrastructure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(app) = %v", err)
	}
	infra := provider.StackRef{Project: spec.Ref.Project, Tier: spec.Ref.Tier, Name: naming.InfraStack("prod")}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: infra}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(environment) = %v", err)
	}

	if !slices.Contains(engine.torn(), naming.ContainerWorkersStack("prod", "web").String()) {
		t.Errorf("torn down = %v, want the workers stack %s", engine.torn(), naming.ContainerWorkersStack("prod", "web"))
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 0 {
		t.Errorf("consumers left = %v, want none: the shared cluster can go once the last service in it has", consumers)
	}
}

func TestReclaimingTheFailedOnlyReleaseOfAnAppHostingContainerWorkersTakesTheirStackAndKeepsTheContainerInfra(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref, KeepParts: true}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	torn := engine.torn()
	if !slices.Contains(torn, naming.ContainerWorkersStack("prod", "web").String()) {
		t.Errorf("torn down = %v, want the workers stack %s: no release of web is left to run them", torn, naming.ContainerWorkersStack("prod", "web"))
	}
	if slices.Contains(torn, containers.StackRef(environment.TierProduction).Name.String()) {
		t.Errorf("torn down = %v, want the container infrastructure kept for the retry", torn)
	}
	if consumers, err := listConsumers(ctx, store); err != nil || len(consumers) != 0 {
		t.Errorf("consumers left = %v (%v), want none: neither stack that claimed the cluster is left", consumers, err)
	}
}

func liveContainerApp(workers ...string) provider.LiveApps {
	return provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeContainer, Workers: workers}}}
}

func TestAContainerAppHostingWorkersKeepsItsWorkersStackAcrossPromotions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, spec := containerStacksHostingWorkers(t, fake.NewKeyValues())
	for deploy := range 2 {
		if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
			t.Fatalf("Provision() of deploy %d = %v", deploy+1, err)
		}
		plan, err := stacks.PlanWorkersRetirement(ctx, liveContainerApp("media"), nil)
		if err != nil {
			t.Fatalf("PlanWorkersRetirement() of deploy %d = %v", deploy+1, err)
		}
		if len(plan.Groups) != 0 {
			t.Errorf("the retirement plan of deploy %d = %v, want nothing: web still hosts its workers on containers", deploy+1, plan.Groups)
		}
		if err := stacks.RetireWorkers(ctx, liveContainerApp("media"), progress.Discard()); err != nil {
			t.Fatalf("RetireWorkers() of deploy %d = %v", deploy+1, err)
		}
	}

	if workers := naming.ContainerWorkersStack("prod", "web").String(); slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v, want the workers stack %s the app still hosts kept", engine.torn(), workers)
	}
}

func TestAContainerAppThatDropsItsWorkersLosesTheirStackOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() with workers = %v", err)
	}
	dropped := spec
	app := *spec.App
	app.Workers, app.DeploymentWorkers, app.Topics = nil, nil, nil
	dropped.App = &app
	dropped.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	engine.previewed = []provider.Change{{Kind: "aws:ecs/service:Service", Name: "media"}}
	workers := naming.ContainerWorkersStack("prod", "web").String()

	plan, err := stacks.Plan(ctx, dropped, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() without workers = %v", err)
	}
	if planGroupFor(plan.Groups, workers, provider.ActionDelete) {
		t.Errorf("the app's plan deletes %s, want it left to the retirement after the promotion", workers)
	}
	if _, err := stacks.Provision(ctx, dropped, progress.Discard()); err != nil {
		t.Fatalf("Provision() without workers = %v", err)
	}
	if slices.Contains(engine.torn(), workers) {
		t.Fatalf("took down %v before the promotion, so a deploy that fails now leaves the live release's tasks unserved", engine.torn())
	}

	retirement, err := stacks.PlanWorkersRetirement(ctx, liveContainerApp(), nil)
	if err != nil {
		t.Fatalf("PlanWorkersRetirement() = %v", err)
	}
	if !planGroupFor(retirement.Groups, workers, provider.ActionDelete) {
		t.Errorf("the retirement plan = %v, want %s deleted", retirement.Groups, workers)
	}
	if err := stacks.RetireWorkers(ctx, liveContainerApp(), progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if !slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v once the promotion served, want %s", engine.torn(), workers)
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(consumers, func(consumer records.Entry) bool { return strings.Contains(consumer.Key.String(), workers) }) {
		t.Errorf("container infrastructure consumers = %v, want the retired workers stack's hold let go", consumers)
	}
}

func TestMovingAnAppFromFunctionsToContainersRetiresItsLambdaWorkersOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	lambda := workersRef(spec.Ref)
	recordWorkersStack(t, store, lambda)

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() on containers = %v", err)
	}
	containers := naming.ContainerWorkersStack("prod", "web").String()
	if slices.Contains(engine.torn(), lambda.Name.String()) || slices.Contains(engine.stacks(), lambda.Name.String()) {
		t.Errorf("ran %v and took down %v before the promotion, want the Lambda workers stack %s left alone while the release on functions serves", engine.stacks(), engine.torn(), lambda.Name)
	}
	if !slices.Contains(engine.stacks(), containers) {
		t.Errorf("ran %v, want the container workers stack %s", engine.stacks(), containers)
	}

	if err := stacks.RetireWorkers(ctx, liveContainerApp("media"), progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if got := engine.torn(); !slices.Equal(got, []string{lambda.Name.String()}) {
		t.Errorf("took down %v once the promotion served, want the Lambda workers stack %s alone", got, lambda.Name)
	}
}

func TestMovingAnAppFromContainersToFunctionsRetiresItsContainerWorkersOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() on containers = %v", err)
	}
	lambda := workersRef(spec.Ref)
	recordWorkersStack(t, store, lambda)
	onFunctions := provider.LiveApps{Slug: spec.Ref.Project, Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless, Workers: []string{"media"}}}}

	plan, err := stacks.PlanWorkersRetirement(ctx, onFunctions, nil)
	if err != nil {
		t.Fatalf("PlanWorkersRetirement() = %v", err)
	}
	containers := naming.ContainerWorkersStack("prod", "web").String()
	var planned []string
	for _, group := range plan.Groups {
		planned = append(planned, group.Name)
	}
	if !slices.Equal(planned, []string{containers}) {
		t.Errorf("the retirement plan deletes %v, want %s alone", planned, containers)
	}
	if err := stacks.RetireWorkers(ctx, onFunctions, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if got := engine.torn(); !slices.Equal(got, []string{containers}) {
		t.Errorf("took down %v, want the container workers stack %s alone", got, containers)
	}
	if got := countRecordedWorkerStacks(t, store, spec.Ref); got != 1 {
		t.Errorf("%d workers stacks are recorded, want the Lambda one alone", got)
	}
}

func TestTheContainersPartIsKeptWhileAContainerWorkersStackRunsInItsCluster(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.containers.Release(ctx, store, spec.Ref, progress.Discard()); err != nil {
		t.Fatalf("let go of the app's own hold: %v", err)
	}

	part, present, err := containers.ReadPart(ctx, store, "ocel", "us-east-1", environment.TierProduction)
	if err != nil || !present {
		t.Fatalf("containers.ReadPart() = %v, %v, want the containers part while the workers' services run in its cluster", present, err)
	}
	if !slices.Equal(part.UsedBy, []string{spec.Ref.Project}) {
		t.Errorf("containers used by %v, want %s, whose workers run there", part.UsedBy, spec.Ref.Project)
	}
	if err := stacks.containers.Remove(ctx, environment.TierProduction, progress.Discard()); err == nil {
		t.Error("containers.Remove() = nil, want it refused while workers services run in the cluster")
	}
	if infra := containers.StackRef(environment.TierProduction).Name.String(); slices.Contains(engine.torn(), infra) {
		t.Fatalf("took down %v, want the shared infrastructure kept", engine.torn())
	}

	if err := stacks.RetireWorkers(ctx, provider.LiveApps{Slug: spec.Ref.Project, Tier: environment.TierProduction, Env: "prod"}, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, containers.Part); err != nil || present {
		t.Errorf("containers record present %v (%v) after the last workers stack went, want it forgotten with the infrastructure", present, err)
	}
}

func planGroupFor(groups []provider.ChangeGroup, stack string, action provider.ChangeAction) bool {
	return slices.ContainsFunc(groups, func(group provider.ChangeGroup) bool {
		return group.Kind == provider.StackGroupKind && group.Name == stack && group.Action == action
	})
}

func TestPlanningAContainerAppHostingWorkersIncludesTheirStack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, spec := containerStacksHostingWorkers(t, fake.NewKeyValues())
	workers := naming.ContainerWorkersStack("prod", "web").String()

	first, err := stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() before the cluster exists = %v", err)
	}
	if !planGroupFor(first.Groups, workers, provider.ActionCreate) {
		t.Errorf("plan groups before the cluster exists = %v, want the workers stack %s created", first.Groups, workers)
	}

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	next, err := stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() once the cluster exists = %v", err)
	}
	if !slices.ContainsFunc(next.Groups, func(group provider.ChangeGroup) bool { return group.Name == workers }) {
		t.Errorf("plan groups = %v, want the workers stack %s the deploy provisions", next.Groups, workers)
	}
}

func TestReclaimingAFailedDeploysContainerReleaseLeavesTheContainerWorkersTheLiveReleaseRuns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() of the live release = %v", err)
	}
	recordRelease(t, store, spec.Ref)
	failed := spec
	failed.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	if _, err := stacks.Provision(ctx, failed, progress.Discard()); err != nil {
		t.Fatalf("Provision() of the failing deploy's release = %v", err)
	}
	recordRelease(t, store, failed.Ref)

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: failed.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	workers := naming.ContainerWorkersStack("prod", "web")
	if slices.Contains(engine.torn(), workers.String()) {
		t.Errorf("took down %v on reclaiming a release that never went live, so the live release's tasks stop being served", engine.torn())
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(consumers, func(consumer records.Entry) bool { return strings.Contains(consumer.Key.String(), workers.String()) }) {
		t.Errorf("container infrastructure consumers = %v, want the live workers stack still holding the cluster", consumers)
	}
}

func TestThePlanToDestroyTheLastContainerReleaseListsItsContainerWorkers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	recordRelease(t, store, spec.Ref)
	engine.previewed = []provider.Change{{Kind: "aws:ecs/service:Service", Name: "media"}}

	plan, err := stacks.PlanDestroy(ctx, spec.Ref, progress.Discard())
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if workers := naming.ContainerWorkersStack("prod", "web").String(); !planGroupFor(plan.Groups, workers, provider.ActionDelete) {
		t.Errorf("plan groups = %v, want the container workers stack %s deleted with the last release", plan.Groups, workers)
	}
}

var webAsItsOwnWorker = provider.WorkerSpec{Name: "web", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func TestProvisioningAContainerAppThatOnlyHostsItsWorkerReturnsTheWorkerAloneAndUsesTheContainersPart(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	spec.App.Workers = []provider.WorkerSpec{webAsItsOwnWorker}
	spec.App.DeploymentWorkers = spec.App.Workers
	spec.App.Topics = []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "web"})}
	delete(engine.outputs, "web")
	engine.outputs[resources.WorkerName("web")] = auto.OutputValue{Value: map[string]any{container.OutputKeyPhysical: container.WorkerServiceName("shop", "prod", "web", "web")}}

	result, err := stacks.Provision(context.Background(), spec, progress.Discard())
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != resources.WorkerName("web") || result.Containers[0].URL != "" {
		t.Errorf("Containers = %+v, want the worker alone, with no URL", result.Containers)
	}
	part, present, err := containers.ReadPart(context.Background(), store, "ocel", "us-east-1", environment.TierProduction)
	if err != nil || !present || !slices.Equal(part.UsedBy, []string{spec.Ref.Project}) {
		t.Errorf("containers part = %+v, %v, %v, want it added and used by %s: its worker's service runs in the cluster", part, present, err, spec.Ref.Project)
	}
}
