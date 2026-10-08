package vps_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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

func recording(machine *box) (*vps.Provider, keyvalue.Store) {
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
	binding, err := p.ProvisionTopic(context.Background(), aTask(t, "send-email"), nil)
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

func TestTheQueuePasswordIsKeptSealedAndRecordedOnlySealed(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionTopic(context.Background(), aTask(t, "send-email"), nil); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	if machine.at(host.KeptPath(environment.TierProduction, queueContainer)) < 0 {
		t.Errorf("the queue's password is not kept on the box:\n%s", strings.Join(machine.commands(), "\n"))
	}
	if !strings.Contains(strings.Join(machine.commands(), "\n"), "'--binding' 'ocel'") {
		t.Error("the password is not sealed to the queue it belongs to")
	}

	entry, err := store.Read(context.Background(), live.QueueDatabaseKey(environment.TierProduction, "shop", "prod"))
	if err != nil {
		t.Fatalf("no queue database is recorded: %v", err)
	}
	var recorded live.QueueDatabase
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Container != queueContainer || recorded.Stack != "prod--infra" {
		t.Errorf("the record names %+v, want the container and the stack the password is sealed to", recorded)
	}
	sealed, err := base64.StdEncoding.DecodeString(recorded.Sealed)
	if err != nil || !strings.HasPrefix(string(sealed), fakeSeal) {
		t.Errorf("the record holds %q, want the password sealed", recorded.Sealed)
	}
}

func TestADeclaredTaskIsRecordedUnderItsDeclaredNameWithItsWholeTopic(t *testing.T) {
	t.Parallel()

	p, store := recording(&box{})
	in := aTask(t, "send-email")
	if _, err := p.ProvisionTopic(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	entry, err := store.Read(context.Background(), live.QueueTopicKey(environment.TierProduction, "shop", "prod", "send-email"))
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
		if _, err := p.ProvisionTopic(context.Background(), aTask(t, name), nil); err != nil {
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
		binding, err := p.ProvisionTopic(context.Background(), in, nil)
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}

	before := len(machine.commands())
	if err := p.RemoveResource(context.Background(), first.Ref, bindings[0], nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	if _, err := store.Read(context.Background(), live.QueueTopicKey(environment.TierProduction, "shop", "prod", "send-email")); err == nil {
		t.Error("the removed task is still recorded, and the engine keeps serving it")
	}
	if strings.Contains(strings.Join(machine.commands()[before:], "\n"), "docker rm --force '"+queueContainer+"'") {
		t.Fatal("the queue database was taken down while another task still runs on it")
	}

	if err := p.RemoveResource(context.Background(), first.Ref, bindings[1], nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	joined := strings.Join(machine.commands()[before:], "\n")
	for _, want := range []string{
		"docker rm --force '" + queueContainer + "'",
		host.KeptPath(environment.TierProduction, queueContainer),
		host.KeptPath(environment.TierProduction, queueContainer+"-"+live.QueueDeliverySecretName),
		host.KeptPath(environment.TierProduction, queueContainer+"-"+live.QueueCallerSecretName),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("removing the last task ran %q and never reached %s", joined, want)
		}
	}
	if _, err := store.Read(context.Background(), live.QueueDatabaseKey(environment.TierProduction, "shop", "prod")); err == nil {
		t.Error("the queue database is still recorded after it was taken down")
	}
}
