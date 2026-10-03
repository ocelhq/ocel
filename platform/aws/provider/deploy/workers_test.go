package deploy

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func workerFunctionOutputs(names map[string]string) auto.OutputMap {
	outputs := auto.OutputMap{}
	for worker, physical := range names {
		outputs[resources.WorkerName(worker)] = auto.OutputValue{Value: map[string]any{outputKeyFunctionName: physical}}
	}
	return outputs
}

func appHostingWorkers(t *testing.T, workers ...string) (*release, provider.StackSpec, *appWork, *mockedEngine) {
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

func TestAProvisionedAppRecordsItsWorkerFunctions(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := appHostingWorkers(t, "media", "ledger")
	engine.outputs = workerFunctionOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media", "ledger": "ocel-app-shop-prod-web-ledger"})

	functions, err := release.provisionWorkers(context.Background(), spec, work, nil)
	if err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	got := map[string]string{}
	for _, fn := range functions {
		got[fn.Name] = fn.Physical
		if fn.URL != "" {
			t.Errorf("%s records URL %q, want none: a worker takes no public traffic", fn.Name, fn.URL)
		}
	}
	if len(got) != 2 || got[resources.WorkerName("media")] != "ocel-app-shop-prod-web-media" || got[resources.WorkerName("ledger")] != "ocel-app-shop-prod-web-ledger" {
		t.Errorf("recorded workers = %v, want both by physical name", got)
	}
}

func TestAnAppHostingNoWorkersRecordsNoWorkerFunctions(t *testing.T) {
	t.Parallel()

	release, spec, work, _ := appHostingWorkers(t)

	functions, err := release.provisionWorkers(context.Background(), spec, work, nil)
	if err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}
	if len(functions) != 0 {
		t.Errorf("recorded %v, want no worker where the app hosts none", functions)
	}
}

func TestAWorkersStackThatExportsNoFunctionNameForAWorkerIsRefused(t *testing.T) {
	t.Parallel()

	release, spec, work, engine := appHostingWorkers(t, "media")
	engine.outputs = auto.OutputMap{}

	if _, err := release.provisionWorkers(context.Background(), spec, work, nil); err == nil {
		t.Error("provisionWorkers() succeeded with no output for the worker, so a deploy would record a worker with no function to read logs from")
	}
}

func TestADeployedAppReturnsItsWorkersBesideItsFunctions(t *testing.T) {
	t.Parallel()

	release, spec, _, engine := appHostingWorkers(t, "media")
	engine.outputs = auto.OutputMap{
		"fn--web--entry": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://web.example/", outputKeyFunctionName: "shop-prod-web-entry"}},
		"fn--web--admin": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://admin.example/", outputKeyFunctionName: "shop-prod-web-admin"}},
	}
	for name, value := range workerFunctionOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"}) {
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
