package vps_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
)

func aWorkerApp() provider.AppSpec {
	app := anApp()
	app.Workers = []provider.WorkerSpec{{Name: "worker"}, {Name: "ledger", Concurrency: 2}}
	app.Values.Bindings = []provider.Binding{{Type: provider.BindingTask, Name: "task--receipt", Resource: "receipt"}}
	app.Values.ContainerEnv = map[string]string{"GREETING": "hello", edge.OriginSecretVar: "edge-only"}
	return app
}

func recordedWorker(t *testing.T, store records.Store, name string) (boxapi.QueueWorker, bool) {
	t.Helper()
	entry, err := store.Read(context.Background(), boxapi.QueueWorkerKey(environment.TierProduction, "shop", "prod", name))
	if err != nil {
		return boxapi.QueueWorker{}, false
	}
	var worker boxapi.QueueWorker
	if err := json.Unmarshal(entry.Value, &worker); err != nil {
		t.Fatal(err)
	}
	return worker, true
}

func releasedWorker(t *testing.T, store records.Store, appContainer, name string) (boxapi.QueueWorker, bool) {
	t.Helper()
	entry, err := store.Read(context.Background(), boxapi.QueueReleaseWorkerKey(environment.TierProduction, "shop", "prod", appContainer, name))
	if err != nil {
		return boxapi.QueueWorker{}, false
	}
	var worker boxapi.QueueWorker
	if err := json.Unmarshal(entry.Value, &worker); err != nil {
		t.Fatal(err)
	}
	return worker, true
}

func releaseOf(t *testing.T, app provider.AppSpec, build string) provider.StackSpec {
	t.Helper()
	app.BuildID = build
	stack := aStack(t, app)
	stack.Ref.Name.Release = naming.NewReleaseToken(build, "")
	return stack
}

func provisionedRelease(t *testing.T, p *vps.Provider, stack provider.StackSpec) string {
	t.Helper()
	started, err := p.ProvisionContainers(context.Background(), stack, progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	return started[0].Physical
}

func promotedWorkers(t *testing.T, p *vps.Provider, appContainer string) {
	t.Helper()
	if err := p.PromoteWorkers(context.Background(), environment.TierProduction, "shop", "prod", "web", appContainer, progress.Discard()); err != nil {
		t.Fatalf("PromoteWorkers(%s) = %v", appContainer, err)
	}
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
	started, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(started) != 1 || started[0].Name != "web" {
		t.Errorf("ProvisionContainers() = %+v, want the app's own container alone recorded as its release", started)
	}
	for _, name := range []string{"worker", "ledger"} {
		worker, found := releasedWorker(t, store, started[0].Physical, name)
		if !found {
			t.Fatalf("worker %s is not recorded as web's release's, so no promotion of it points the engine at it", name)
		}
		if worker.App != "web" || worker.Stack != "prod--web--r0a1b2c3d" || worker.Container != "shop-prod-web-"+name+"-r0a1b2c3d" {
			t.Errorf("worker %s is recorded as %+v, want a container of its own in web's release", name, worker)
		}
		run := runOf(machine, worker.Container)
		if run == "" {
			t.Fatalf("worker %s's container %s was never started:\n%s", name, worker.Container, strings.Join(machine.commands(), "\n"))
		}
		if !strings.Contains(run, " '"+loadedImageRef+"' >/dev/null") {
			t.Errorf("worker %s runs %q, want the app's own image: its runtime runs the worker entry OCEL_WORKER asks for", name, run)
		}
		if !strings.Contains(run, "'--network' 'ocel-prod-shop'") || strings.Contains(run, "--publish") {
			t.Errorf("worker %s runs %q, want it on the project network and published nowhere", name, run)
		}
	}
	if ledger, _ := releasedWorker(t, store, started[0].Physical, "ledger"); ledger.Concurrency != 2 {
		t.Errorf("ledger is recorded with concurrency %d, want 2", ledger.Concurrency)
	}
}

func TestAWorkerIsHandedItsNameAndTheAppsValuesButNeverTheEdgesSecret(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	ledger, _ := releasedWorker(t, store, provisionedRelease(t, p, aStack(t, aWorkerApp())), "ledger")
	handed := machine.fedTo("install -m 0600 /dev/stdin '" + envfile.Path(environment.TierProduction, ledger.Container) + "'")
	for _, want := range []string{"OCEL_WORKER=ledger", "GREETING=hello", boxapi.EnvVar + "="} {
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
	ledger, _ := releasedWorker(t, store, provisionedRelease(t, p, aStack(t, aWorkerApp())), "ledger")
	for _, command := range machine.commands() {
		if strings.Contains(command, boxapi.RoutingTablePath) && strings.Contains(command, ledger.Container) {
			t.Errorf("the worker reached the routing table: %s", command)
		}
	}
}

const nextBuildID = "fedcba9876543210fedcba9876543210"

func TestADeployThatFailsAfterItsWorkersStartedLeavesThePreviousReleasesWorkersRecordedAndRunning(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	promotedWorkers(t, p, provisionedRelease(t, p, aStack(t, aWorkerApp())))
	serving, _ := recordedWorker(t, store, "worker")

	before := len(machine.commands())
	provisionedRelease(t, p, releaseOf(t, aWorkerApp(), nextBuildID))

	if after, found := recordedWorker(t, store, "worker"); !found || after != serving {
		t.Errorf("the engine delivers to %+v (recorded %t) once a release no promotion served started its workers, want %+v: the app still serves the previous release", after, found, serving)
	}
	for _, command := range machine.since(before) {
		if strings.Contains(command, "docker stop '"+serving.Container+"'") || strings.Contains(command, "docker rm --force '"+serving.Container+"'") {
			t.Errorf("starting the next release's workers took down %s, which the serving release still runs:\n%s", serving.Container, command)
		}
	}
	if state := machine.containers[serving.Container]; !strings.HasPrefix(state, "running ") {
		t.Errorf("%s is %q, want it running", serving.Container, state)
	}
}

func TestPromotingAReleasePointsTheWorkersAtItsContainersAndStopsTheOnesItReplaces(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	first := provisionedRelease(t, p, aStack(t, aWorkerApp()))
	promotedWorkers(t, p, first)
	second := provisionedRelease(t, p, releaseOf(t, aWorkerApp(), nextBuildID))
	promotedWorkers(t, p, second)

	for _, name := range []string{"worker", "ledger"} {
		want, _ := releasedWorker(t, store, second, name)
		if got, _ := recordedWorker(t, store, name); got != want {
			t.Errorf("worker %s is recorded as %+v, want the promoted release's %+v", name, got, want)
		}
		replaced, _ := releasedWorker(t, store, first, name)
		if state := machine.containers[replaced.Container]; !strings.HasPrefix(state, "exited ") {
			t.Errorf("the replaced worker %s is %q, want it stopped and kept for a rollback", replaced.Container, state)
		}
	}
}

func TestRollingBackToAnOlderReleaseStartsItsWorkersAgainAndPointsTheRecordsAtThem(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	first := provisionedRelease(t, p, aStack(t, aWorkerApp()))
	promotedWorkers(t, p, first)
	second := provisionedRelease(t, p, releaseOf(t, aWorkerApp(), nextBuildID))
	promotedWorkers(t, p, second)

	before := len(machine.commands())
	promotedWorkers(t, p, first)

	for _, name := range []string{"worker", "ledger"} {
		want, _ := releasedWorker(t, store, first, name)
		if got, _ := recordedWorker(t, store, name); got != want {
			t.Errorf("after the rollback worker %s is recorded as %+v, want the older release's %+v", name, got, want)
		}
		if !slices.ContainsFunc(machine.since(before), func(command string) bool {
			return strings.Contains(command, "docker start '"+want.Container+"'")
		}) {
			t.Errorf("the rollback never started %s again", want.Container)
		}
		if state := machine.containers[want.Container]; !strings.HasPrefix(state, "running ") {
			t.Errorf("%s is %q after the rollback, want it running", want.Container, state)
		}
		demoted, _ := releasedWorker(t, store, second, name)
		if state := machine.containers[demoted.Container]; !strings.HasPrefix(state, "exited ") {
			t.Errorf("the demoted worker %s is %q, want it stopped", demoted.Container, state)
		}
	}
}

func TestAPromotedReleaseThatDropsAWorkerStopsItAndARollbackRecordsItAgain(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	first := provisionedRelease(t, p, aStack(t, aWorkerApp()))
	promotedWorkers(t, p, first)
	dropped, _ := releasedWorker(t, store, first, "ledger")
	next := aWorkerApp()
	next.Workers = next.Workers[:1]
	promotedWorkers(t, p, provisionedRelease(t, p, releaseOf(t, next, nextBuildID)))

	if recorded, found := recordedWorker(t, store, "ledger"); found {
		t.Errorf("ledger is recorded as %+v under a release that declares no ledger, so the engine delivers to code the app no longer serves", recorded)
	}
	if state := machine.containers[dropped.Container]; !strings.HasPrefix(state, "exited ") {
		t.Errorf("the dropped worker %s is %q, want it stopped and kept for a rollback", dropped.Container, state)
	}

	promotedWorkers(t, p, first)
	if recorded, _ := recordedWorker(t, store, "ledger"); recorded != dropped {
		t.Errorf("after the rollback ledger is recorded as %+v, want %+v", recorded, dropped)
	}
}

func TestRemovingARetiredReleaseLeavesTheWorkersANewerOneRuns(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	firstStack := aStack(t, aWorkerApp())
	first := provisionedRelease(t, p, firstStack)
	promotedWorkers(t, p, first)
	secondStack := releaseOf(t, aWorkerApp(), nextBuildID)
	second := provisionedRelease(t, p, secondStack)
	promotedWorkers(t, p, second)
	current, _ := recordedWorker(t, store, "worker")
	retired, _ := releasedWorker(t, store, first, "worker")

	before := len(machine.commands())
	if err := p.RemoveContainers(context.Background(), firstStack.Ref, []provider.AppContainer{{Name: "web", Physical: first}}, nil, progress.Discard()); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if after, found := recordedWorker(t, store, "worker"); !found || after != current {
		t.Errorf("removing the retired release changed the newer release's worker to %+v, %v", after, found)
	}
	if !slices.ContainsFunc(machine.since(before), func(command string) bool {
		return strings.Contains(command, "docker rm --force '"+retired.Container+"'")
	}) {
		t.Errorf("removing the retired release left its worker %s on the box", retired.Container)
	}
	if _, found := releasedWorker(t, store, first, "worker"); found {
		t.Error("the retired release still records its worker after it was removed")
	}

	before = len(machine.commands())
	if err := p.RemoveContainers(context.Background(), secondStack.Ref, []provider.AppContainer{{Name: "web", Physical: second}}, nil, progress.Discard()); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if !slices.ContainsFunc(machine.since(before), func(command string) bool {
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
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, anApp()), progress.Discard()); err != nil {
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
	if _, err := p.ProvisionTopic(context.Background(), aTask(t, "receipt"), progress.Discard()); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	entry, err := store.Read(context.Background(), boxapi.QueueDatabaseKey(environment.TierProduction, "shop", "prod"))
	if err != nil {
		t.Fatal(err)
	}
	var database boxapi.QueueDatabase
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

	ledger, _ := releasedWorker(t, store, provisionedRelease(t, p, aStack(t, aWorkerApp())), "ledger")
	handed := machine.fedTo("install -m 0600 /dev/stdin '" + envfile.Path(environment.TierProduction, ledger.Container) + "'")
	if !strings.Contains(handed, edge.OriginSecretVar+"="+string(secret)) {
		t.Errorf("ledger is handed %q, want %s set to the queue's delivery secret, so its front refuses a POST the engine did not send", handed, edge.OriginSecretVar)
	}
	if !strings.Contains(handed, edge.OriginFrontedVar+"=") {
		t.Errorf("ledger is handed %q, want %s, so a worker whose delivery secret is lost refuses to start rather than admit any container on the project network", handed, edge.OriginFrontedVar)
	}
}
