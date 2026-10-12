package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	pulumi "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func newWorkerOutputs(names map[string]string) auto.OutputMap {
	outputs := auto.OutputMap{}
	for worker, physical := range names {
		outputs[resources.WorkerName(worker)] = auto.OutputValue{Value: map[string]any{outputKeyFunctionName: physical}}
	}
	return outputs
}

func newReleaseHostingWorkers(t *testing.T, workers ...string) (*release, provider.StackSpec, *appWork, *mockedEngine) {
	t.Helper()
	cfg, spec := appStackSpec(t)
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "us-east-1"
	cfg.ImageOptimizerURL = ""
	cfg.Objects = &fakeArtifactStore{exists: map[string]bool{}}
	cfg.EdgeAccessKeyID, cfg.EdgeSecretKey = "AKIAEDGE", "edge-secret"
	cfg.KeyValues = fake.NewKeyValues()
	for _, worker := range workers {
		spec.App.Workers = append(spec.App.Workers, provider.WorkerSpec{Name: worker})
	}
	spec.App.Functions[0].HasWorkerEntry = true
	engine := &mockedEngine{}
	release := releasingOn(t, cfg, engine)
	work, err := release.appWork(spec, nil, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	return release, spec, work, engine
}

func TestProvisioningAnAppsWorkersReturnsEachWorkerFunctionByPhysicalName(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t, "media", "ledger")
	engine.outputs = newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media", "ledger": "ocel-app-shop-prod-web-ledger"})

	functions, err := release.provisionWorkers(context.Background(), spec, work, nil)
	if err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	got := map[string]string{}
	for _, fn := range functions {
		got[fn.Name] = fn.Physical
		if fn.URL != "" {
			t.Errorf("%s returns URL %q, want none: a worker takes no public traffic", fn.Name, fn.URL)
		}
	}
	if len(got) != 2 || got[resources.WorkerName("media")] != "ocel-app-shop-prod-web-media" || got[resources.WorkerName("ledger")] != "ocel-app-shop-prod-web-ledger" {
		t.Errorf("returned workers = %v, want both by physical name", got)
	}
}

func TestProvisioningTheWorkersOfAnAppHostingNoneReturnsNoFunctions(t *testing.T) {
	t.Parallel()

	release, spec, work, _ := newReleaseHostingWorkers(t)

	functions, err := release.provisionWorkers(context.Background(), spec, work, nil)
	if err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	if len(functions) != 0 {
		t.Errorf("returned %v, want no worker where the app hosts none", functions)
	}
}

func TestAWorkersStackThatExportsNoFunctionNameForAWorkerIsRefused(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = auto.OutputMap{}

	if _, err := release.provisionWorkers(context.Background(), spec, work, nil); err == nil {
		t.Error("provisionWorkers() succeeded with no output for the worker, so a deploy would record a worker with no function to read logs from")
	}
}

func TestAProvisionedAppReturnsItsWorkersBesideItsFunctions(t *testing.T) {
	t.Parallel()

	release, spec, _, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = auto.OutputMap{
		"fn--web--entry": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://web.example/", outputKeyFunctionName: "shop-prod-web-entry"}},
		"fn--web--admin": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://admin.example/", outputKeyFunctionName: "shop-prod-web-admin"}},
	}
	for name, value := range newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"}) {
		engine.outputs[name] = value
	}

	result, err := release.provision(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("provision() = %v", err)
	}
	physical := map[string]string{}
	for _, fn := range result.Functions {
		physical[fn.Name] = fn.Physical
	}
	if physical["fn--web--entry"] != "shop-prod-web-entry" || physical[resources.WorkerName("media")] != "ocel-app-shop-prod-web-media" || len(physical) != 3 {
		t.Errorf("Functions = %v, want the app's two functions and its media worker, each by physical name", physical)
	}
}

func TestTheWorkersProgramExportsEachWorkersLambdaNameForTheStackToDecode(t *testing.T) {
	t.Parallel()

	work := &workersWork{
		project: "shop",
		stack:   workersStack("prod", "web"),
		workers: []provider.WorkerSpec{{Name: "media"}, {Name: "ledger"}},
		args:    functionArgs{Runtime: defaultFunctionRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024},
		role:    executionRole{App: "web", Boundary: testBoundaryARN},
		region:  "us-east-1",
		account: mockAccount,
		table:   testStateTableARN,
		prefix:  naming.TaskKeyPrefix("shop", "prod"),
	}
	rec := &inputRecorder{}
	exported := make(chan map[string]any, 1)
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error {
		if err := work.run(pctx); err != nil {
			return err
		}
		pulumi.Map(pctx.GetCurrentExportMap()).ToMapOutput().ApplyT(func(resolved map[string]any) error {
			exported <- resolved
			return nil
		})
		return nil
	}, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	lambdas := map[string]string{}
	for _, name := range rec.registered("aws:lambda/function:Function") {
		inputs := rec.inputs("aws:lambda/function:Function", name)
		lambdas[inputs["environment"].ObjectValue()["variables"].ObjectValue()["OCEL_WORKER"].StringValue()] = inputs["name"].StringValue()
	}
	outputs := auto.OutputMap{}
	for logical, value := range <-exported {
		outputs[logical] = auto.OutputValue{Value: value}
	}
	for _, worker := range []string{"media", "ledger"} {
		fields, _ := outputs[resources.WorkerName(worker)].Value.(map[string]any)
		if lambdas[worker] == "" || fields[outputKeyFunctionName] != lambdas[worker] {
			t.Errorf("export %s = %v, want the name of %s's Lambda, %q", resources.WorkerName(worker), fields, worker, lambdas[worker])
		}
	}
	decoded, err := work.decode(outputs)
	if err != nil {
		t.Fatalf("decode the program's own exports: %v", err)
	}
	physical := map[string]string{}
	for _, fn := range decoded.Functions {
		physical[fn.Name] = fn.Physical
	}
	if len(physical) != 2 || physical[resources.WorkerName("media")] != lambdas["media"] || physical[resources.WorkerName("ledger")] != lambdas["ledger"] {
		t.Errorf("decoded %v, want each worker by its Lambda's name %v", physical, lambdas)
	}
}

func countRecordedWorkerStacks(t *testing.T, store keyvalue.Store, ref provider.StackRef) int {
	t.Helper()
	entries, err := store.List(context.Background(), stackrecords.StacksPartition(ref.Tier, ref.Project), workersKey)
	if err != nil {
		t.Fatalf("list the recorded workers stacks: %v", err)
	}
	return len(entries)
}

func recordWorkersStack(t *testing.T, store keyvalue.Store, ref provider.StackRef) {
	t.Helper()
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: workersAt(ref), Value: []byte("{}")}); err != nil {
		t.Fatalf("record the workers stack of %s: %v", ref.Name, err)
	}
}

func provisionedWebOutputs() auto.OutputMap {
	outputs := auto.OutputMap{
		"fn--web--entry": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://web.example/", outputKeyFunctionName: "shop-prod-web-entry"}},
		"fn--web--admin": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://admin.example/", outputKeyFunctionName: "shop-prod-web-admin"}},
	}
	for name, value := range newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"}) {
		outputs[name] = value
	}
	return outputs
}

func TestADeployWhoseReleaseDropsItsWorkersAndFailsBeforePromoteLeavesTheLiveReleasesWorkersStack(t *testing.T) {
	t.Parallel()

	release, spec, _, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = provisionedWebOutputs()
	ctx := context.Background()
	if _, err := release.provision(ctx, spec, nil); err != nil {
		t.Fatalf("provision() of the live release = %v", err)
	}
	recordRelease(t, release.cfg.KeyValues, spec.Ref)
	dropped := *spec.App
	dropped.Workers = nil
	failed := spec
	failed.App = &dropped
	failed.Ref.Name = naming.AppStack(spec.Ref.Name.Env, "web", mustReleaseToken(t, "r00000009"))

	if _, err := release.provision(ctx, failed, nil); err != nil {
		t.Fatalf("provision() of the release that drops its workers = %v", err)
	}
	recordRelease(t, release.cfg.KeyValues, failed.Ref)
	if err := release.Destroy(ctx, failed.Ref, nil, nil); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	workers := workersRef(spec.Ref)
	if slices.Contains(engine.torn(), workers.Name.String()) {
		t.Errorf("took down %v before any promotion stopped needing %s, so the live release's queues and schedules stop being served", engine.torn(), workers.Name)
	}
	if got := countRecordedWorkerStacks(t, release.cfg.KeyValues, spec.Ref); got != 1 {
		t.Errorf("%d workers stacks are recorded, want the live release's", got)
	}
}

func TestAnAppThatNeverHostedWorkersTakesDownNoStack(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t)

	if _, err := release.provisionWorkers(context.Background(), spec, work, nil); err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	if got := engine.torn(); len(got) != 0 {
		t.Errorf("took down %v, want nothing where the app never hosted workers", got)
	}
}

func mustReleaseToken(t *testing.T, value string) naming.ReleaseToken {
	t.Helper()
	token, err := naming.ParseReleaseToken(value)
	if err != nil {
		t.Fatalf("ParseReleaseToken(%q) = %v", value, err)
	}
	return token
}

func TestPlanningAnAppThatHostsWorkersIncludesTheWorkersStack(t *testing.T) {
	t.Parallel()

	release, spec, _, _ := newReleaseHostingWorkers(t, "media")

	plan, err := release.plan(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}
	workers := workersStack(spec.Ref.Name.Env, "web").String()
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Kind == provider.StackGroupKind && group.Name == workers && group.Action == provider.ActionCreate
	}) {
		t.Errorf("plan groups = %v, want a group creating the workers stack %s the deploy provisions", plan.Groups, workers)
	}
}

func recordWorkersStacks(t *testing.T, store keyvalue.Store, stacks ...naming.StackName) {
	t.Helper()
	for _, stack := range stacks {
		recordWorkersStack(t, store, provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: stack})
	}
}

func liveInProd(apps ...provider.LiveApp) provider.LiveApps {
	return provider.LiveApps{Slug: "conformance", Tier: environment.TierProduction, Env: "prod", Apps: apps}
}

func TestRetiringWorkersTakesDownTheWorkersStackOfEachAppNoLongerDeployedToTheEnvironment(t *testing.T) {
	t.Parallel()

	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.KeyValues = fake.NewKeyValues()
	engine := &mockedEngine{}
	recordWorkersStacks(t, cfg.KeyValues, workersStack("prod", "web"), workersStack("prod", "jobs"), workersStack("staging", "jobs"))
	log := &fake.Log{}

	if err := stacksWith(cfg, engine).RetireWorkers(context.Background(), liveInProd(provider.LiveApp{App: "web", Compute: provider.ComputeServerless, Workers: []string{"media"}}), log); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}

	retired := workersStack("prod", "jobs").String()
	if got := engine.torn(); !slices.Equal(got, []string{retired}) {
		t.Errorf("took down %v, want only %s, whose app this environment no longer deploys", got, retired)
	}
	if got := countRecordedWorkerStacks(t, cfg.KeyValues, provider.StackRef{Project: "conformance", Tier: environment.TierProduction}); got != 2 {
		t.Errorf("%d workers stacks are recorded, want the live app's and the other environment's", got)
	}
	if !slices.ContainsFunc(log.Lines(), func(line string) bool { return strings.Contains(line, retired) }) {
		t.Errorf("the deploy said %q, want a line naming the workers stack it removed", log.Lines())
	}
}

func TestThePlanToRetireWorkersDeletesEachStackRetiringTakesDown(t *testing.T) {
	t.Parallel()

	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.KeyValues = fake.NewKeyValues()
	engine := &mockedEngine{}
	recordWorkersStacks(t, cfg.KeyValues, workersStack("prod", "web"), workersStack("prod", "jobs"), workersStack("staging", "jobs"))

	plan, err := stacksWith(cfg, engine).PlanWorkersRetirement(context.Background(), liveInProd(), nil)
	if err != nil {
		t.Fatalf("PlanWorkersRetirement() = %v", err)
	}
	var deleted []string
	for _, group := range plan.Groups {
		if group.Kind == provider.StackGroupKind && group.Action == provider.ActionDelete {
			deleted = append(deleted, group.Name)
		}
	}
	slices.Sort(deleted)
	if want := []string{workersStack("prod", "jobs").String(), workersStack("prod", "web").String()}; !slices.Equal(deleted, want) {
		t.Errorf("plan deletes %v, want every workers stack of the environment with no app live %v", deleted, want)
	}
	if got := engine.torn(); len(got) != 0 {
		t.Errorf("planning took down %v, want nothing", got)
	}
}

func TestThePlanToDestroyAReleaseShowsTheStacksDestroyTakesDown(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"})
	engine.previewed = []provider.Change{{Kind: "aws:lambda/function:Function", Name: "fn"}}
	ctx := context.Background()
	if _, err := release.provisionWorkers(ctx, spec, work, nil); err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}

	plan, err := release.PlanDestroy(ctx, spec.Ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if err := release.Destroy(ctx, spec.Ref, nil, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	var shown []string
	for _, group := range plan.Groups {
		shown = append(shown, group.Name)
	}
	slices.Sort(shown)
	torn := engine.torn()
	slices.Sort(torn)
	if !slices.Equal(shown, torn) {
		t.Errorf("PlanDestroy() shows %v and Destroy() took down %v, and a teardown is consented to by what it shows", shown, torn)
	}
}

func TestProvisioningAnAppMovedToContainerComputeLeavesItsWorkersStackUntilAPromotionStopsNeedingIt(t *testing.T) {
	t.Parallel()

	stacks, engine, _, spec := retainingContainers(t)
	ref := workersRef(spec.Ref)
	opened, err := stacks.at(context.Background(), spec.Ref, edgeKindOf(spec))
	if err != nil {
		t.Fatalf("open the release: %v", err)
	}
	recordWorkersStack(t, opened.cfg.KeyValues, ref)

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if slices.Contains(engine.torn(), ref.Name.String()) {
		t.Errorf("took down %v while the release on functions still serves, so its queues and schedules stop being served", engine.torn())
	}
}

func TestReclaimingAFailedDeploysReleaseLeavesTheWorkersTheLiveReleaseRuns(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"})
	ctx := context.Background()
	if _, err := release.provisionWorkers(ctx, spec, work, nil); err != nil {
		t.Fatalf("provisionWorkers() of the live release = %v", err)
	}
	recordRelease(t, release.cfg.KeyValues, spec.Ref)
	failed := spec
	failed.Ref.Name = naming.AppStack(spec.Ref.Name.Env, "web", mustReleaseToken(t, "r00000009"))
	if _, err := release.provisionWorkers(ctx, failed, work, nil); err != nil {
		t.Fatalf("provisionWorkers() of the failing deploy's release = %v", err)
	}

	if err := release.Destroy(ctx, failed.Ref, nil, nil); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	if workers := workersRef(spec.Ref).Name.String(); slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v on reclaiming a release that never went live, so the live release's queues and schedules stop being served", engine.torn())
	}
}

func recordRelease(t *testing.T, store keyvalue.Store, ref provider.StackRef) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: ref.Name.App}); err != nil {
		t.Fatalf("record the release %s: %v", ref.Name, err)
	}
}

func TestReclaimingTheOnlyReleaseOfAnAppTakesDownItsWorkersStack(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := newReleaseHostingWorkers(t, "media")
	engine.outputs = newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"})
	ctx := context.Background()
	if _, err := release.provisionWorkers(ctx, spec, work, nil); err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	recordRelease(t, release.cfg.KeyValues, spec.Ref)

	if err := release.Destroy(ctx, spec.Ref, nil, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	if workers := workersRef(spec.Ref).Name.String(); !slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v, want %s too: no release of web is left to run its workers, so they would consume its queues on code nothing serves", engine.torn(), workers)
	}
	if got := countRecordedWorkerStacks(t, release.cfg.KeyValues, spec.Ref); got != 0 {
		t.Errorf("%d workers stacks are still recorded after the only release of their app went", got)
	}
}

func TestRetiringWorkersTakesDownTheWorkersStackOfALiveAppWhosePromotedReleaseNeedsNone(t *testing.T) {
	t.Parallel()

	for name, web := range map[string]provider.LiveApp{
		"it hosts no workers any more":  {App: "web", Compute: provider.ComputeServerless},
		"it moved to container compute": {App: "web", Compute: provider.ComputeContainer, Workers: []string{"media"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := conformingConfig(&fakeArtifactStore{})
			cfg.KeyValues = fake.NewKeyValues()
			engine := &mockedEngine{}
			recordWorkersStacks(t, cfg.KeyValues, workersStack("prod", "web"), workersStack("prod", "jobs"))
			stacks := stacksWith(cfg, engine)
			live := liveInProd(web, provider.LiveApp{App: "jobs", Compute: provider.ComputeServerless, Workers: []string{"mail"}})
			log := &fake.Log{}

			plan, err := stacks.PlanWorkersRetirement(context.Background(), live, nil)
			if err != nil {
				t.Fatalf("PlanWorkersRetirement() = %v", err)
			}
			if err := stacks.RetireWorkers(context.Background(), live, log); err != nil {
				t.Fatalf("RetireWorkers() = %v", err)
			}

			retired := workersStack("prod", "web").String()
			var planned []string
			for _, group := range plan.Groups {
				if group.Kind == provider.StackGroupKind && group.Action == provider.ActionDelete {
					planned = append(planned, group.Name)
				}
			}
			if !slices.Equal(planned, []string{retired}) {
				t.Errorf("the plan deletes %v, want %s alone, the stack the promoted release of web no longer runs", planned, retired)
			}
			if got := engine.torn(); !slices.Equal(got, []string{retired}) {
				t.Errorf("took down %v, want %s alone", got, retired)
			}
			if got := countRecordedWorkerStacks(t, cfg.KeyValues, provider.StackRef{Project: "conformance", Tier: environment.TierProduction}); got != 1 {
				t.Errorf("%d workers stacks are recorded, want jobs' alone", got)
			}
			if !slices.ContainsFunc(log.Lines(), func(line string) bool { return strings.Contains(line, retired) && strings.Contains(line, "web") }) {
				t.Errorf("the deploy said %q, want a line naming the workers stack it removed and why", log.Lines())
			}
		})
	}
}

func TestAppWorkersRunFromTheFunctionWhoseArtifactCarriesTheWorkerEntry(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.Region = "us-east-1"
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	spec.App.Functions[1].HasWorkerEntry = true
	release := releasingOn(t, cfg, &mockedEngine{})
	work, err := release.appWork(spec, nil, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	hosted, err := release.workersWork(spec, work)
	if err != nil {
		t.Fatalf("workersWork() = %v", err)
	}
	if want := spec.App.Functions[1].Artifact.Key; hosted.artifact.Key != want {
		t.Errorf("the workers run from %s, want %s, the one artifact that carries the worker entry", hosted.artifact.Key, want)
	}
}

func TestAppWorkersAreRefusedWhenNoFunctionCarriesTheWorkerEntry(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.Region = "us-east-1"
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	release := releasingOn(t, cfg, &mockedEngine{})
	work, err := release.appWork(spec, nil, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	if _, err := release.workersWork(spec, work); err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("workersWork() = %v, want a refusal naming the worker no function can start", err)
	}
}
