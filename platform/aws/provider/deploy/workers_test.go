package deploy

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	pulumi "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
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
	cfg.CacheStoreBucket = "isr"
	cfg.CacheStoreObjects = &fakeArtifactStore{exists: map[string]bool{}}
	for _, worker := range workers {
		spec.App.Workers = append(spec.App.Workers, provider.WorkerSpec{Name: worker})
	}
	engine := &mockedEngine{}
	release := releasingOn(t, cfg, engine)
	work, err := release.appWork(spec, nil)
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
