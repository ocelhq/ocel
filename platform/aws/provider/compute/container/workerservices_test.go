package container

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

const testStateTableARN = "arn:aws:dynamodb:us-east-1:" + mockAccount + ":table/ocel-state"

func TestOnlyAWorkerOnContainerComputeNamesTheServiceASenderWakes(t *testing.T) {
	t.Parallel()

	got, err := WorkerServices("ocel", environment.TierProduction, "shop", "prod", []provider.WorkerSpec{
		{Name: "media", Concurrency: 4, App: "jobs", Compute: provider.ComputeContainer},
		{Name: "worker", App: "web", Compute: provider.ComputeServerless},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("containerWorkers() = %+v, want only the media worker: a function is woken by its queue, not by a sender", got)
	}
	media := got["media"]
	if media.Concurrency != 4 {
		t.Errorf("media concurrency = %d, want 4: the engine holds a worker's cap in slots", media.Concurrency)
	}
	want := &queues.Service{Cluster: awsports.ContainerClusterName("ocel", environment.TierProduction), Name: workerService("shop", "prod", "jobs", "media")}
	if media.Service == nil || *media.Service != *want {
		t.Errorf("media service = %+v, want %+v", media.Service, want)
	}
}

func TestAContainerWorkersServiceNameIsOneForEachWorkerOfEachAppAndEnvironment(t *testing.T) {
	t.Parallel()

	names := map[string]bool{}
	for _, name := range []string{
		workerService("shop", "prod", "jobs", "media"),
		workerService("shop", "prod", "jobs", "reports"),
		workerService("shop", "prod", "web", "media"),
		workerService("shop", "staging", "jobs", "media"),
		workerService("other", "prod", "jobs", "media"),
	} {
		if len(name) > 255 || names[name] {
			t.Errorf("service name %q repeats an earlier one or is longer than ECS allows", name)
		}
		names[name] = true
	}
}

func TestAnAppReachingTasksMayWakeExactlyTheContainerWorkersOfTheDeploymentAndNoOtherService(t *testing.T) {
	t.Parallel()

	topics := mustTopics(t, []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "media"})})
	workers := []provider.WorkerSpec{
		{Name: "media", App: "jobs", Compute: provider.ComputeContainer},
		{Name: "worker", App: "web", Compute: provider.ComputeServerless},
	}
	table := topic.StateTable{Region: "us-east-1", ARN: testStateTableARN}
	wakes, err := WorkerServiceARNs(table, "ocel", environment.TierProduction, "shop", "prod", workers)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := topic.TasksPolicy(table, "shop", "prod", topics, wakes)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Statement []struct {
			Action   any `json:"Action"`
			Resource any `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatalf("the policy %s is not JSON: %v", policy, err)
	}
	service := "arn:aws:ecs:us-east-1:" + mockAccount + ":service/ocel-prod-containers/" + workerService("shop", "prod", "jobs", "media")
	var found bool
	for _, statement := range document.Statement {
		actions, _ := json.Marshal(statement.Action)
		if !strings.Contains(string(actions), "ecs:UpdateService") {
			continue
		}
		found = true
		if got, _ := json.Marshal(statement.Resource); string(got) != `["`+service+`"]` {
			t.Errorf("ecs:UpdateService is granted on %s, want only %s: a function-hosted worker is woken by its queue", got, service)
		}
		if !strings.Contains(string(actions), "ecs:DescribeServices") || strings.Contains(string(actions), "*") {
			t.Errorf("wake actions %s, want DescribeServices and UpdateService alone", actions)
		}
	}
	if !found {
		t.Errorf("the tasks policy %s grants no ecs:UpdateService, so a send could never start a worker sitting at no tasks", policy)
	}

	policy, err = topic.TasksPolicy(table, "shop", "prod", topics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(policy, "ecs:") {
		t.Errorf("a deployment with no container worker was granted %s", policy)
	}
}

var shopNames = queues.Names{Namespace: "ocel", Project: "shop", Env: "prod"}

func mustTopics(t *testing.T, resources []provider.Resource) topic.Topics {
	t.Helper()
	topics, err := topic.Of(shopNames, resources)
	if err != nil {
		t.Fatal(err)
	}
	return topics
}

func queueName(t *testing.T, topic, consumer string, fifo bool) string {
	t.Helper()
	named, err := shopNames.Queues(topic, consumer, fifo)
	if err != nil {
		t.Fatal(err)
	}
	return named.Queue
}

func workerService(project, env, app, worker string) string {
	name, err := WorkerServiceName(shopNames.Namespace, project, env, app, worker)
	if err != nil {
		panic(err)
	}
	return name
}
