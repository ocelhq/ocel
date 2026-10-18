package vps_test

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const queueContainer = "shop-prod-infra-ocel-queue"

func aTask(t *testing.T, name string) resources.ProvisionRequest {
	t.Helper()
	stack, err := naming.ParseStackName("prod--infra")
	if err != nil {
		t.Fatal(err)
	}
	return resources.ProvisionRequest{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Resource: provider.Resource{
			Name: "task--" + name, Declared: name, Type: provider.BindingTask,
			Topic: &provider.TopicSpec{
				Cron: "0 * * * *",
				Consumers: []provider.ConsumerSpec{{
					Name: name, Worker: "worker", Exclusive: true,
					Retry: provider.RetryPolicy{MaxAttempts: 5, MinDelay: 2 * time.Second, MaxDelay: time.Minute},
					Lanes: []provider.Lane{provider.LaneHigh},
					Batch: &provider.BatchPolicy{Size: 10},
				}},
			},
		},
	}
}

func recording(machine *box) (*vps.Provider, records.Store) {
	p := over(machine)
	store := fake.NewKeyValues()
	p.Recording(store)
	return p, store
}

func TestAProviderOverABoxRunsTopicsTasksAndWorkersOnContainers(t *testing.T) {
	t.Parallel()

	facts := over(&box{}).Facts()
	for _, served := range []provider.BindingType{provider.BindingTopic, provider.BindingTask} {
		if !slices.Contains(facts.Bindings, served) {
			t.Errorf("Facts().Bindings = %v, want %s served", facts.Bindings, served)
		}
	}
	want := []provider.WorkerCeiling{{Compute: provider.ComputeContainer, Unbounded: true}}
	if !slices.Equal(facts.WorkerCeilings, want) {
		t.Errorf("Facts().WorkerCeilings = %+v, want %+v: a box runs a worker for as long as a run takes", facts.WorkerCeilings, want)
	}
}

func TestADeclaredTaskRunsTheEnvironmentsQueueDatabaseAsAConfinedContainerOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	binding, err := p.ProvisionTopic(context.Background(), aTask(t, "send-email"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	if binding.Type != provider.BindingTask || binding.Name != "task--send-email" || binding.Resource != "send-email" {
		t.Errorf("the binding is %s %q of %q, want the task the app declared", binding.Type, binding.Name, binding.Resource)
	}

	run := machine.at("'docker' 'run'")
	if run < 0 {
		t.Fatalf("nothing was started:\n%s", strings.Join(machine.commands(), "\n"))
	}
	runCommand := machine.commands()[run]
	for _, want := range []string{
		"'--name' '" + queueContainer + "'",
		"'--network' 'ocel-production-shop'",
		"ocel.resource=ocel",
		"'ocel.backup=pg'",
		"'--cap-drop' 'ALL'",
		images.QueueDatabase(),
		"'listen_addresses='",
		"dst=" + boxapi.QueueSocketsPath,
		"'" + docker.LabelOwner + "=" + boxapi.QueueRole + "'",
	} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the queue database was started without %q:\n%s", want, runCommand)
		}
	}
	for _, refused := range []string{"--publish", "'-p'"} {
		if strings.Contains(runCommand, refused) {
			t.Errorf("the queue database was started with %s:\n%s", refused, runCommand)
		}
	}
}

func TestTheAgentReachesTheQueueAsARoleThatOwnsOnlyItsDatabaseAndHoldsNoPassword(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionTopic(context.Background(), aTask(t, "send-email"), progress.Discard()); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	if machine.at(container.KeptPath(environment.TierProduction, queueContainer)) < 0 {
		t.Errorf("the queue's superuser password is not kept on the box:\n%s", strings.Join(machine.commands(), "\n"))
	}
	granted := ""
	for at, command := range machine.commands() {
		if strings.Contains(command, "'psql'") {
			granted = machine.fed[at]
		}
	}
	for _, want := range []string{"CREATE ROLE " + boxapi.QueueRole + " LOGIN", "NOSUPERUSER", "ALTER DATABASE " + boxapi.QueueDatabaseName + " OWNER TO " + boxapi.QueueRole} {
		if !strings.Contains(granted, want) {
			t.Errorf("the queue database was handed %q, want it to hold %q", granted, want)
		}
	}

	entry, err := store.Read(context.Background(), boxapi.QueueDatabaseKey(environment.TierProduction, "shop", "prod"))
	if err != nil {
		t.Fatalf("no queue database is recorded: %v", err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded["container"] != queueContainer || recorded["stack"] != "prod--infra" {
		t.Errorf("the record names %+v, want the container and the stack", recorded)
	}
	if _, holds := recorded["sealed"]; holds {
		t.Errorf("the record holds the superuser's password for the agent, which reaches the queue as %s and needs none: %+v", boxapi.QueueRole, recorded)
	}
}

func TestADeclaredTaskIsRecordedUnderItsDeclaredNameWithItsWholeTopic(t *testing.T) {
	t.Parallel()

	p, store := recording(&box{})
	in := aTask(t, "send-email")
	if _, err := p.ProvisionTopic(context.Background(), in, progress.Discard()); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	entry, err := store.Read(context.Background(), boxapi.QueueTopicKey(environment.TierProduction, "shop", "prod", "send-email"))
	if err != nil {
		t.Fatalf("the task is not recorded under its declared name: %v", err)
	}
	recorded := &contractv1.ManifestTopic{}
	if err := protojson.Unmarshal(entry.Value, recorded); err != nil {
		t.Fatal(err)
	}
	want := &contractv1.ManifestTopic{
		Cron: "0 * * * *",
		Consumers: []*contractv1.ManifestConsumer{{
			Name: "send-email", Worker: "worker", Exclusive: true,
			Retry: &resourcesv1.RetryPolicy{MaxAttempts: 5, MinDelay: durationpb.New(2 * time.Second), MaxDelay: durationpb.New(time.Minute)},
			Lanes: []topicv1.Lane{topicv1.Lane_LANE_HIGH},
			Batch: &resourcesv1.BatchPolicy{Size: 10},
		}},
	}
	if !proto.Equal(recorded, want) {
		t.Errorf("the task is recorded as %v, want %v: the engine runs the consumer's resolved retry, lanes and batch", recorded, want)
	}
}

func TestEveryTopicOfAnEnvironmentSharesOneQueueDatabase(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	for _, name := range []string{"send-email", "resize"} {
		if _, err := p.ProvisionTopic(context.Background(), aTask(t, name), progress.Discard()); err != nil {
			t.Fatalf("ProvisionTopic(%s) = %v", name, err)
		}
	}
	started := 0
	for _, command := range machine.commands() {
		if strings.Contains(command, "'docker' 'run'") && strings.Contains(command, queueContainer) {
			started++
		}
	}
	if started != 1 {
		t.Errorf("the queue database was started %d times for two tasks, want once", started)
	}
}

func TestARemovedTaskIsForgottenAndTheLastTakesTheQueueDatabaseWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	first, second := aTask(t, "send-email"), aTask(t, "resize")
	var bindings []provider.Binding
	for _, in := range []resources.ProvisionRequest{first, second} {
		binding, err := p.ProvisionTopic(context.Background(), in, progress.Discard())
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}

	before := len(machine.commands())
	if err := p.RemoveTopic(context.Background(), first.Ref, bindings[0], progress.Discard()); err != nil {
		t.Fatalf("RemoveTopic() = %v", err)
	}
	if _, err := store.Read(context.Background(), boxapi.QueueTopicKey(environment.TierProduction, "shop", "prod", "send-email")); err == nil {
		t.Error("the removed task is still recorded, and the engine keeps serving it")
	}
	if strings.Contains(strings.Join(machine.commands()[before:], "\n"), "docker rm --force '"+queueContainer+"'") {
		t.Fatal("the queue database was taken down while another task still runs on it")
	}

	if err := p.RemoveTopic(context.Background(), first.Ref, bindings[1], progress.Discard()); err != nil {
		t.Fatalf("RemoveTopic() = %v", err)
	}
	joined := strings.Join(machine.commands()[before:], "\n")
	for _, want := range []string{
		"docker rm --force '" + queueContainer + "'",
		container.KeptPath(environment.TierProduction, queueContainer),
		container.KeptPath(environment.TierProduction, queueContainer+"-"+boxapi.QueueDeliverySecretName),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("removing the last task ran %q and never reached %s", joined, want)
		}
	}
	if _, err := store.Read(context.Background(), boxapi.QueueDatabaseKey(environment.TierProduction, "shop", "prod")); err == nil {
		t.Error("the queue database is still recorded after it was taken down")
	}
}

func plannedTopic(t *testing.T, machine *box, store *fake.KeyValues, in resources.ProvisionRequest) (provider.ChangeAction, string) {
	t.Helper()
	mark, recorded := len(machine.commands()), store.Snapshot()
	p := over(machine)
	p.Recording(store)
	action, reason, err := p.PlanTopic(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("PlanTopic() = %v", err)
	}
	refuseWrites(t, machine.since(mark))
	if !maps.Equal(store.Snapshot(), recorded) {
		t.Errorf("planning a topic wrote a record, and a plan records nothing")
	}
	return action, reason
}

func topicProvisioned(t *testing.T, machine *box, store *fake.KeyValues, in resources.ProvisionRequest) {
	t.Helper()
	p := over(machine)
	p.Recording(store)
	if _, err := p.ProvisionTopic(context.Background(), in, progress.Discard()); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
}

func TestAnUnchangedTaskPlansKeep(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	topicProvisioned(t, machine, store, aTask(t, "send-email"))
	if action, reason := plannedTopic(t, machine, store, aTask(t, "send-email")); action != provider.ActionKeep {
		t.Errorf("a task declared as it was provisioned plans %s %q, want keep", action, reason)
	}
}

func TestATaskNeverRecordedPlansACreate(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	if action, reason := plannedTopic(t, machine, store, aTask(t, "send-email")); action != provider.ActionCreate {
		t.Errorf("a task on a box that never ran one plans %s %q, want create", action, reason)
	}
	topicProvisioned(t, machine, store, aTask(t, "send-email"))
	if action, reason := plannedTopic(t, machine, store, aTask(t, "resize")); action != provider.ActionCreate {
		t.Errorf("a second task beside a running queue database plans %s %q, want create", action, reason)
	}
}

func TestATaskWhoseScheduleChangesPlansAnUpdateNamingBothSchedules(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	topicProvisioned(t, machine, store, aTask(t, "send-email"))
	moved := aTask(t, "send-email")
	moved.Resource.Topic.Cron = "30 * * * *"
	action, reason := plannedTopic(t, machine, store, moved)
	if action != provider.ActionUpdate || !strings.Contains(reason, `"0 * * * *"`) || !strings.Contains(reason, `"30 * * * *"`) {
		t.Errorf("a task moved to another schedule plans %s %q, and the apply records the new schedule", action, reason)
	}
}

func TestATaskWhoseQueueDatabaseStoppedPlansAnUpdate(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	topicProvisioned(t, machine, store, aTask(t, "send-email"))
	machine.containers[queueContainer] = strings.Replace(machine.containers[queueContainer], "running", "exited", 1)
	action, reason := plannedTopic(t, machine, store, aTask(t, "send-email"))
	if action != provider.ActionUpdate || !strings.Contains(reason, queueContainer) {
		t.Errorf("a task whose queue database exited plans %s %q, and the apply starts a new one", action, reason)
	}
}
