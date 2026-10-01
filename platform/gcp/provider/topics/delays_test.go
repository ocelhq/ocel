package topics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"connectrpc.com/connect"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const publisher = "ocel-preview@floci-local.iam.gserviceaccount.com"

func newDelayed(t *testing.T) published {
	t.Helper()
	p := newPublished(t)
	p.deployment.Delays = topics.Delays{
		Queue:      "projects/" + p.deployment.Clients.Project + "/locations/" + p.deployment.Clients.Region + "/queues/" + strings.ReplaceAll(p.deployment.Names.Scope.Environment, ".", "-"),
		Account:    publisher,
		PublishURL: "https://pubsub.googleapis.com",
	}
	if err := p.deployment.Delays.Ensure(context.Background(), p.deployment.Clients); err != nil {
		t.Fatalf("Ensure() of the delay queue = %v", err)
	}
	return p
}

func (p published) delayTasks() []*cloudtaskspb.Task {
	p.t.Helper()
	client, err := p.deployment.Clients.CloudTasks()
	if err != nil {
		p.t.Fatal(err)
	}
	listed := client.ListTasks(context.Background(), &cloudtaskspb.ListTasksRequest{Parent: p.deployment.Delays.Queue, ResponseView: cloudtaskspb.Task_FULL})
	var tasks []*cloudtaskspb.Task
	for {
		task, err := listed.Next()
		if errors.Is(err, iterator.Done) {
			return tasks
		}
		if err != nil {
			p.t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
}

func (p published) dispatch(task *cloudtaskspb.Task) {
	p.t.Helper()
	request := task.GetHttpRequest()
	path := strings.TrimPrefix(request.GetUrl(), p.deployment.Delays.PublishURL)
	resp, err := http.Post(p.endpoint+path, "application/json", bytes.NewReader(request.GetBody()))
	if err != nil {
		p.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("publishing what the delay task carries answered %d", resp.StatusCode)
	}
}

func TestLiveADelayedTriggerWaitsInCloudTasksAndIsPublishedAtItsDueTime(t *testing.T) {
	p := newDelayed(t)
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	id := p.trigger("resize", exactJSON, &taskv1.TriggerOptions{DueAt: timestamppb.New(due)})

	if run := p.retrieve(id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || !run.GetDueAt().AsTime().Equal(due) {
		t.Fatalf("the run is %s due %v, want delayed until %v", run.GetStatus(), run.GetDueAt().AsTime(), due)
	}
	if pulled := p.pull("resize", "resize"); len(pulled) != 0 {
		t.Fatalf("the task's subscription already holds %d messages, want none before the run is due", len(pulled))
	}
	tasks := p.delayTasks()
	if len(tasks) != 1 {
		t.Fatalf("the delay queue holds %d tasks, want 1", len(tasks))
	}
	delay := tasks[0]
	if !delay.GetScheduleTime().AsTime().Equal(due) {
		t.Errorf("the delay task is scheduled for %v, want %v", delay.GetScheduleTime().AsTime(), due)
	}
	if want := "https://pubsub.googleapis.com/v1/projects/" + p.deployment.Clients.Project + "/topics/" + p.deployment.Names.Topic("resize") + ":publish"; delay.GetHttpRequest().GetUrl() != want {
		t.Errorf("the delay task posts to %s, want %s", delay.GetHttpRequest().GetUrl(), want)
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(delay.GetHttpRequest().GetBody(), &body); err != nil || len(body.Messages) != 1 {
		t.Fatalf("the delay task carries %s, want a Pub/Sub publish of one message", delay.GetHttpRequest().GetBody())
	}

	p.dispatch(delay)
	if codes := p.deliverPulled(worker, "resize", "resize"); len(codes) != 1 || !acked(codes[0]) {
		t.Fatalf("once published the task's subscription delivered %v, want one acked push", codes)
	}
	if run := p.retrieve(id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED {
		t.Errorf("the run is %s after its delayed message was delivered, want completed", run.GetStatus())
	}
}

func TestLiveADelayedSendWaitsInCloudTasks(t *testing.T) {
	p := newDelayed(t)
	due := time.Now().Add(time.Hour)

	if _, err := p.deployment.Topics().Send(context.Background(), &topicv1.SendRequest{Topic: "orders", Payload: []byte(exactJSON), DueAt: timestamppb.New(due)}); err != nil {
		t.Fatal(err)
	}

	if pulled := p.pull("orders", "ship"); len(pulled) != 0 {
		t.Errorf("a consumer's subscription already holds %d messages, want none before the message is due", len(pulled))
	}
	tasks := p.delayTasks()
	if len(tasks) != 1 {
		t.Fatalf("the delay queue holds %d tasks, want 1", len(tasks))
	}
	p.dispatch(tasks[0])
	pulled := p.pull("orders", "ship")
	if len(pulled) != 1 || pulled[0].payload() != exactJSON {
		t.Errorf("once published the consumer's subscription holds %d messages, want the one sent with %s", len(pulled), exactJSON)
	}
}

func TestLiveRescheduleMovesADelayedRunsTaskAndRefusesARunNoLongerDelayed(t *testing.T) {
	p := newDelayed(t)
	ctx := context.Background()
	id := p.trigger("resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	later := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	resp, err := p.deployment.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(later)})
	if err != nil {
		t.Fatalf("RescheduleRun() = %v", err)
	}
	if run := resp.GetRun(); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || !run.GetDueAt().AsTime().Equal(later) {
		t.Errorf("the rescheduled run is %s due %v, want delayed until %v", run.GetStatus(), run.GetDueAt().AsTime(), later)
	}
	tasks := p.delayTasks()
	if len(tasks) != 1 || !tasks[0].GetScheduleTime().AsTime().Equal(later) {
		t.Errorf("the delay queue holds %d tasks, want only one, scheduled for %v", len(tasks), later)
	}

	queued := p.trigger("resize", `{}`, nil)
	if _, err := p.deployment.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: queued, DueAt: timestamppb.New(later)}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("RescheduleRun() of a queued run = %v, want %s: only a delayed run can be rescheduled", err, connect.CodeFailedPrecondition)
	}
	if _, err := p.deployment.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: "nothing", DueAt: timestamppb.New(later)}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("RescheduleRun() of no run = %v, want %s", err, connect.CodeNotFound)
	}
}

func TestLiveACanceledRunIsNeverAttempted(t *testing.T) {
	p := newDelayed(t)
	ctx := context.Background()
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	delayed := p.trigger("resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	queued := p.trigger("resize", `{}`, nil)

	for _, id := range []string{delayed, queued} {
		resp, err := p.deployment.Tasks().CancelRun(ctx, &taskv1.CancelRunRequest{Id: id})
		if err != nil {
			t.Fatalf("CancelRun(%s) = %v", id, err)
		}
		if run := resp.GetRun(); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED || run.GetFinishedAt() == nil {
			t.Errorf("the canceled run is %s, want canceled with a finish time", run.GetStatus())
		}
	}
	if tasks := p.delayTasks(); len(tasks) != 0 {
		t.Errorf("the delay queue still holds %d tasks, want the canceled run's deleted", len(tasks))
	}
	if codes := p.deliverPulled(worker, "resize", "resize"); len(codes) != 1 || !acked(codes[0]) {
		t.Errorf("the queued run's message delivered %v, want one acked push", codes)
	}
	if len(worker.received()) != 0 {
		t.Error("a canceled run reached the worker")
	}

	again, err := p.deployment.Tasks().CancelRun(ctx, &taskv1.CancelRunRequest{Id: queued})
	if err != nil || again.GetRun().GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED {
		t.Errorf("CancelRun() of a canceled run = %v, %v, want it answered as it is", again.GetRun().GetStatus(), err)
	}
	if _, err := p.deployment.Tasks().CancelRun(ctx, &taskv1.CancelRunRequest{Id: "nothing"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("CancelRun() of no run = %v, want %s", err, connect.CodeNotFound)
	}
}
