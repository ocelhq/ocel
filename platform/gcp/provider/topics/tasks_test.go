package topics_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func (p published) trigger(task, payload string, options *taskv1.TriggerOptions) string {
	p.t.Helper()
	resp, err := p.deployment.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(payload), Options: options})
	if err != nil {
		p.t.Fatalf("Trigger(%s) = %v", task, err)
	}
	return resp.GetId()
}

func (p published) retrieve(id string) *taskv1.Run {
	p.t.Helper()
	resp, err := p.deployment.Tasks().RetrieveRun(context.Background(), &taskv1.RetrieveRunRequest{Id: id})
	if err != nil {
		p.t.Fatalf("RetrieveRun(%s) = %v", id, err)
	}
	return resp.GetRun()
}

func TestLiveATriggeredRunIsQueuedAtOnceAndCompletesWhenItsMessageIsDelivered(t *testing.T) {
	p := newPublished(t)
	worker := newFakeWorker(t, always(http.StatusOK, exactJSON))

	id := p.trigger("resize", exactJSON, &taskv1.TriggerOptions{Tags: []string{"eu", "big"}, Metadata: []byte(`{"by":"test"}`)})

	run := p.retrieve(id)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || run.GetTask() != "resize" {
		t.Fatalf("the run is %s of task %q, want queued for resize", run.GetStatus(), run.GetTask())
	}
	if string(run.GetPayload()) != exactJSON || string(run.GetMetadata()) != `{"by":"test"}` || !slices.Equal(run.GetTags(), []string{"eu", "big"}) {
		t.Errorf("the run holds payload %s, metadata %s and tags %v, want what the trigger gave byte for byte", run.GetPayload(), run.GetMetadata(), run.GetTags())
	}

	if codes := p.deliverPulled(worker, "resize", "resize"); len(codes) != 1 || !acked(codes[0]) {
		t.Fatalf("the task's subscription delivered %v, want one acked push", codes)
	}
	run = p.retrieve(id)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || string(run.GetOutput()) != exactJSON || run.GetAttempts() != 1 {
		t.Errorf("the run is %s after %d attempts with output %s, want completed after one with %s", run.GetStatus(), run.GetAttempts(), run.GetOutput(), exactJSON)
	}
	if !slices.Equal(run.GetTags(), []string{"eu", "big"}) {
		t.Errorf("the delivered run lost its tags: %v", run.GetTags())
	}
}

func TestLiveATriggerThatLowersMaxAttemptsCarriesItToTheWorker(t *testing.T) {
	p := newPublished(t)

	p.trigger("resize", `{}`, &taskv1.TriggerOptions{MaxAttempts: 1})

	pulled := p.pull("resize", "resize")
	if len(pulled) != 1 || pulled[0].Message.Attributes[topics.MaxAttemptsAttribute] != "1" {
		t.Errorf("the task's subscription holds %d messages, want one naming maxAttempts 1", len(pulled))
	}
}

func TestLiveATriggerWithALiveIdempotencyKeyAnswersTheFirstRun(t *testing.T) {
	p := newPublished(t)

	first := p.trigger("resize", `{"n":1}`, &taskv1.TriggerOptions{IdempotencyKey: "image-7"})
	second := p.trigger("resize", `{"n":2}`, &taskv1.TriggerOptions{IdempotencyKey: "image-7"})

	if second != first {
		t.Errorf("the second trigger answered run %s, want the first's %s", second, first)
	}
	if pulled := p.pull("resize", "resize"); len(pulled) != 1 {
		t.Errorf("the task's subscription holds %d messages, want only the first", len(pulled))
	}
}

func TestLiveATriggerThatFailsToStartLeavesItsIdempotencyKeyFree(t *testing.T) {
	p := newPublished(t)
	options := &taskv1.TriggerOptions{IdempotencyKey: "image-7", DueAt: timestamppb.New(time.Now().Add(time.Hour))}

	if resp, err := p.deployment.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{}`), Options: options}); err == nil {
		t.Fatalf("Trigger() with no delay queue answered %s, want it to fail", resp.GetId())
	}

	p = p.withDelays()
	id := p.trigger("resize", `{}`, options)
	if run := p.retrieve(id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED {
		t.Errorf("the retried trigger answered a run that is %s, want a delayed run of its own", run.GetStatus())
	}
	if tasks := p.delayTasks(); len(tasks) != 1 {
		t.Errorf("the delay queue holds %d tasks, want the retried trigger's", len(tasks))
	}
}

func TestLiveATriggerOrRetrieveOfNothingDeployedIsRefused(t *testing.T) {
	p := newPublished(t)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		req  *taskv1.TriggerRequest
		code connect.Code
	}{
		"no such task":           {&taskv1.TriggerRequest{Task: "nothing", Payload: []byte(`{}`)}, connect.CodeNotFound},
		"a topic":                {&taskv1.TriggerRequest{Task: "orders", Payload: []byte(`{}`)}, connect.CodeNotFound},
		"a payload not JSON":     {&taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{`)}, connect.CodeInvalidArgument},
		"metadata not an object": {&taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{}`), Options: &taskv1.TriggerOptions{Metadata: []byte(`[1]`)}}, connect.CodeInvalidArgument},
	} {
		if _, err := p.deployment.Tasks().Trigger(ctx, tc.req); connect.CodeOf(err) != tc.code {
			t.Errorf("Trigger() of %s = %v, want %s", name, err, tc.code)
		}
	}
	if _, err := p.deployment.Tasks().RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: "nothing"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("RetrieveRun() of no run = %v, want %s", err, connect.CodeNotFound)
	}
}
