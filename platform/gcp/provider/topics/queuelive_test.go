//go:build integration

package topics_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/api/iterator"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func delayedQueue(t *testing.T) topics.Queue {
	t.Helper()
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	queue := topics.Queue{Clients: clients, Names: names, Topics: deployedTopics(), Delays: topics.Delays{
		Queue:      "projects/" + clients.Project + "/locations/" + clients.Region + "/queues/" + strings.ReplaceAll(names.Scope.Environment, ".", "-"),
		Account:    "ocel-preview@floci-local.iam.gserviceaccount.com",
		PublishURL: topics.PublishURL,
	}}
	if err := queue.Delays.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() of the delay queue = %v", err)
	}
	return queue
}

func delayTasks(t *testing.T, queue topics.Queue) []*cloudtaskspb.Task {
	t.Helper()
	client, err := queue.Clients.CloudTasks()
	if err != nil {
		t.Fatal(err)
	}
	listed := client.ListTasks(context.Background(), &cloudtaskspb.ListTasksRequest{Parent: queue.Delays.Queue, ResponseView: cloudtaskspb.Task_FULL})
	var tasks []*cloudtaskspb.Task
	for {
		task, err := listed.Next()
		if errors.Is(err, iterator.Done) {
			return tasks
		}
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
}

func TestLiveAMessageDueLaterWaitsInCloudTasksAsAPublishOfItselfAtItsDueTime(t *testing.T) {
	queue := delayedQueue(t)
	now := time.Now().UTC()
	due := now.Add(time.Hour).Truncate(time.Second)
	sent := runs.Message{Topic: "resize", Consumer: "resize", Execution: "01K0000000000000000000000A-resize", Delivery: "01K0000000000000000000000D", ID: "01K0000000000000000000000A", PublishedAt: now, DueAt: due}

	for range 2 {
		if err := queue.Send(context.Background(), sent); err != nil {
			t.Fatalf("Send() = %v", err)
		}
	}

	tasks := delayTasks(t, queue)
	if len(tasks) != 1 {
		t.Fatalf("the delay queue holds %d tasks after sending one delivery twice, want 1", len(tasks))
	}
	delay := tasks[0]
	if !delay.GetScheduleTime().AsTime().Equal(due) {
		t.Errorf("the delay task is scheduled for %v, want %v", delay.GetScheduleTime().AsTime(), due)
	}
	if want := topics.PublishURL + "/v1/" + topics.TopicPath(queue.Clients.Project, topicOf(t, queue.Names, "resize")) + ":publish"; delay.GetHttpRequest().GetUrl() != want {
		t.Errorf("the delay task posts to %s, want %s", delay.GetHttpRequest().GetUrl(), want)
	}
	var body pubsub.PublishRequest
	if err := json.Unmarshal(delay.GetHttpRequest().GetBody(), &body); err != nil || len(body.Messages) != 1 {
		t.Fatalf("the delay task carries %s, want a Pub/Sub publish of one message", delay.GetHttpRequest().GetBody())
	}
	attributes := body.Messages[0].Attributes
	if attributes[topics.DeliveryAttribute] != sent.Delivery || attributes[topics.ExecutionAttribute] != sent.Execution {
		t.Errorf("the delayed message carries %v, want its execution and the delivery that makes it stale once the run is rescheduled", attributes)
	}
	if data, _ := base64.StdEncoding.DecodeString(body.Messages[0].Data); len(data) != 0 {
		t.Errorf("the delayed message carries %s, want no payload: its run holds it", data)
	}
}
