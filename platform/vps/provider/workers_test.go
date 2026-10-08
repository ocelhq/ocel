package vps_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func aWorkerApp() provider.AppSpec {
	app := anApp()
	app.Workers = []provider.WorkerSpec{{Name: "worker"}, {Name: "ledger", Concurrency: 2}}
	app.Values.Bindings = []provider.Binding{{Type: provider.BindingTask, Name: "task--receipt", Resource: "receipt"}}
	app.Values.ContainerEnv = map[string]string{"GREETING": "hello", originguard.OriginSecretVar: "edge-only"}
	return app
}

func recordedWorker(t *testing.T, store keyvalue.Store, name string) (live.QueueWorker, bool) {
	t.Helper()
	entry, err := store.Read(context.Background(), live.QueueWorkerKey(environment.TierProduction, "shop", "prod", name))
	if err != nil {
		return live.QueueWorker{}, false
	}
	var worker live.QueueWorker
	if err := json.Unmarshal(entry.Value, &worker); err != nil {
		t.Fatal(err)
	}
	return worker, true
}

func runOf(machine *box, container string) string {
	for _, command := range machine.commands() {
		if strings.Contains(command, "'docker' 'run'") && strings.Contains(command, "'--name' '"+container+"'") {
			return command
		}
	}
	return ""
}

func TestEachWorkerJoiningAnAppRunsAsASecondContainerFromItsImage(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	started, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(started) != 1 || started[0].Name != "web" {
		t.Errorf("ProvisionContainers() = %+v, want the app's own container alone recorded as its release", started)
	}
	for _, name := range []string{"worker", "ledger"} {
		worker, found := recordedWorker(t, store, name)
		if !found {
			t.Fatalf("worker %s is not recorded, so the engine never delivers to it", name)
		}
		if worker.App != "web" || worker.Stack != "prod--web--r0a1b2c3d" || worker.Container == "" || worker.Container == started[0].Physical {
			t.Errorf("worker %s is recorded as %+v, want a container of its own in web's release", name, worker)
		}
		run := runOf(machine, worker.Container)
		if run == "" {
			t.Fatalf("worker %s's container %s was never started:\n%s", name, worker.Container, strings.Join(machine.commands(), "\n"))
		}
		if !strings.Contains(run, " '"+loadedImageRef+"' >/dev/null") {
			t.Errorf("worker %s runs %q, want the app's own image: its runtime runs the worker entry OCEL_WORKER asks for", name, run)
		}
		if !strings.Contains(run, "'--network' 'ocel-production-shop'") || strings.Contains(run, "--publish") {
			t.Errorf("worker %s runs %q, want it on the project network and published nowhere", name, run)
		}
	}
	if ledger, _ := recordedWorker(t, store, "ledger"); ledger.Concurrency != 2 {
		t.Errorf("ledger is recorded with concurrency %d, want 2", ledger.Concurrency)
	}
}

func TestAWorkerIsHandedItsNameAndTheAppsValuesButNeverTheEdgesSecret(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	ledger, _ := recordedWorker(t, store, "ledger")
	handed := machine.fedTo("install -m 0600 /dev/stdin '" + host.EnvFile(environment.TierProduction, ledger.Container) + "'")
	for _, want := range []string{"OCEL_WORKER=ledger", "GREETING=hello", live.EnvVar + "="} {
		if !strings.Contains(handed, want) {
			t.Errorf("ledger is handed %q, want %s in it", handed, want)
		}
	}
	for _, refused := range []string{"edge-only", originguard.HealthPathVar} {
		if strings.Contains(handed, refused) {
			t.Errorf("ledger is handed %s: the engine, not the edge, calls a worker, and it answers no health probe", refused)
		}
	}
	if !strings.Contains(handed, `"queue":"prod"`) {
		t.Errorf("ledger's live manifest %q names no queue, so its triggers reach no engine", handed)
	}
}

func TestAWorkerNeverReachesTheRoutingTable(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatal(err)
	}
	ledger, _ := recordedWorker(t, store, "ledger")
	for _, command := range machine.commands() {
		if strings.Contains(command, live.RoutingTable) && strings.Contains(command, ledger.Container) {
			t.Errorf("the worker reached the routing table: %s", command)
		}
	}
}

func TestANewReleaseTakesDownTheWorkersItReplacesAndTheOnesNoLongerDeclared(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatal(err)
	}
	old, _ := recordedWorker(t, store, "worker")
	retired, _ := recordedWorker(t, store, "ledger")

	next := aWorkerApp()
	next.BuildID = "fedcba9876543210fedcba9876543210"
	next.Workers = next.Workers[:1]
	before := len(machine.commands())
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, next), nil); err != nil {
		t.Fatal(err)
	}
	current, _ := recordedWorker(t, store, "worker")
	if current.Container == old.Container {
		t.Fatalf("the new release recorded the old worker container %s", old.Container)
	}
	joined := strings.Join(machine.commands()[before:], "\n")
	for _, gone := range []string{old.Container, retired.Container} {
		if !strings.Contains(joined, "docker rm --force '"+gone+"'") {
			t.Errorf("the new release left %s running:\n%s", gone, joined)
		}
	}
	if _, found := recordedWorker(t, store, "ledger"); found {
		t.Error("ledger is still recorded after the release that dropped it, so the engine keeps delivering to a container that is gone")
	}
}

func TestRemovingARetiredReleaseLeavesTheWorkersANewerOneRuns(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	first := aStack(t, aWorkerApp())
	if _, err := p.ProvisionContainers(context.Background(), first, nil); err != nil {
		t.Fatal(err)
	}
	next := aWorkerApp()
	next.BuildID = "fedcba9876543210fedcba9876543210"
	second := aStack(t, next)
	second.Ref.Name.Release = naming.NewReleaseToken(next.BuildID, "")
	if _, err := p.ProvisionContainers(context.Background(), second, nil); err != nil {
		t.Fatal(err)
	}
	current, _ := recordedWorker(t, store, "worker")

	if err := p.RemoveContainers(context.Background(), first.Ref, nil, nil); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if after, found := recordedWorker(t, store, "worker"); !found || after != current {
		t.Errorf("removing the retired release changed the newer release's worker to %+v, %v", after, found)
	}

	before := len(machine.commands())
	if err := p.RemoveContainers(context.Background(), second.Ref, nil, nil); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if !slices.ContainsFunc(machine.commands()[before:], func(command string) bool {
		return strings.Contains(command, "docker rm --force '"+current.Container+"'")
	}) {
		t.Error("removing the release that runs the worker left its container running")
	}
	if _, found := recordedWorker(t, store, "worker"); found {
		t.Error("the worker is still recorded after its release was removed")
	}
}

func TestAnAppNoWorkerJoinsStartsNoWorker(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, anApp()), nil); err != nil {
		t.Fatal(err)
	}
	runs := 0
	for _, command := range machine.commands() {
		if strings.Contains(command, "'docker' 'run'") {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("an app with no worker started %d containers, want its own alone", runs)
	}
}

func TestAWorkerAdmitsOnlyDeliveriesSignedWithItsEnvironmentsDeliverySecret(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionTopic(context.Background(), aTask(t, "receipt"), nil); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	entry, err := store.Read(context.Background(), live.QueueDatabaseKey(environment.TierProduction, "shop", "prod"))
	if err != nil {
		t.Fatal(err)
	}
	var database live.QueueDatabase
	if err := json.Unmarshal(entry.Value, &database); err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString(database.DeliverySealed)
	if err != nil || !strings.HasPrefix(string(sealed), fakeSeal) {
		t.Fatalf("the queue records delivery secret %q, want it sealed by the box", database.DeliverySealed)
	}
	secret, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(sealed), fakeSeal))
	if err != nil || len(secret) == 0 {
		t.Fatalf("the sealed delivery secret %q opens to nothing", sealed)
	}

	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	ledger, _ := recordedWorker(t, store, "ledger")
	handed := machine.fedTo("install -m 0600 /dev/stdin '" + host.EnvFile(environment.TierProduction, ledger.Container) + "'")
	if !strings.Contains(handed, originguard.OriginSecretVar+"="+string(secret)) {
		t.Errorf("ledger is handed %q, want %s set to the queue's delivery secret, so its front refuses a POST the engine did not send", handed, originguard.OriginSecretVar)
	}
}
