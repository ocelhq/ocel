package tasks

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/runs/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func idleConsumption(t *testing.T, queue *fakeSQS, services *fakeServices, workerURL string, topics map[string]queues.Topic) *Consumption {
	t.Helper()
	cfg := Config{Topology: queues.Topology{Topics: topics, Workers: containerWorkers()}, Queues: queue, Topics: &fakeSNS{}, Services: services, Worker: "media", WorkerURL: workerURL}
	c := NewConsumption(newEngine(cfg, fake.NewStore(), nil), queue)
	c.idleAfter, c.idleEvery = 30*time.Millisecond, 5*time.Millisecond
	return c
}

func runUntil(t *testing.T, c *Consumption, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- c.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the condition never held")
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatalf("Run() = %v, want nil once told to stop", err)
	}
}

func emptyQueues() map[string]string {
	return map[string]string{"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0", "ApproximateNumberOfMessagesDelayed": "7"}
}

func resizeOnly() map[string]queues.Topic {
	return map[string]queues.Topic{"resize": aTask("resize", retrying(3), false)}
}

func TestAWorkerWithNothingToDoSetsItsServiceToNoTasksOnceItsQueuesHaveStayedEmpty(t *testing.T) {
	t.Parallel()

	queue, services := newFakeSQS(), &fakeServices{desired: 1}
	queue.counts = emptyQueues()
	c := idleConsumption(t, queue, services, answering(t, answerAlways(http.StatusOK)), resizeOnly())

	runUntil(t, c, func() bool { return len(services.updates()) > 0 })

	got := services.updates()[0]
	if aws.ToString(got.Cluster) != "ocel-containers-production" || aws.ToString(got.Service) != "ocel-app-media" || aws.ToInt32(got.DesiredCount) != 0 {
		t.Errorf("UpdateService(%s, %s, desired %d), want the worker's own service at none: delayed messages wait unseen and the backlog alarm wakes it when they are due", aws.ToString(got.Cluster), aws.ToString(got.Service), aws.ToInt32(got.DesiredCount))
	}
}

func TestAnIdleWorkerWhoseAppSetsAFloorScalesItsServiceInToThatFloor(t *testing.T) {
	t.Parallel()

	queue, services := newFakeSQS(), &fakeServices{desired: 3}
	queue.counts = emptyQueues()
	c := idleConsumption(t, queue, services, answering(t, answerAlways(http.StatusOK)), resizeOnly())
	c.engine.queue.services.workers["media"].Service.Floor = 2

	runUntil(t, c, func() bool { return len(services.updates()) > 0 })

	if got := aws.ToInt32(services.updates()[0].DesiredCount); got != 2 {
		t.Errorf("UpdateService(desired %d), want the floor of 2 its app's minInstances sets", got)
	}
}

func TestAWorkerStaysUpWhileAnyOfItsQueuesHoldsAMessageVisibleOrInFlightAnywhere(t *testing.T) {
	t.Parallel()

	for name, counts := range map[string]map[string]string{
		"visible":   {"ApproximateNumberOfMessages": "1", "ApproximateNumberOfMessagesNotVisible": "0"},
		"in flight": {"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "3"},
	} {
		queue, services := newFakeSQS(), &fakeServices{desired: 1}
		queue.counts = counts
		c := idleConsumption(t, queue, services, answering(t, answerAlways(http.StatusOK)), resizeOnly())

		runUntil(t, c, func() bool { queue.mu.Lock(); defer queue.mu.Unlock(); return queue.queried >= 3 })

		if updated := services.updates(); len(updated) != 0 {
			t.Errorf("%s: the service was set to %d tasks while a queue still held a message, which another task may be working", name, aws.ToInt32(updated[0].DesiredCount))
		}
	}
}

func TestAWorkerKeepsItsServiceUpWhileItIsWorkingAMessage(t *testing.T) {
	t.Parallel()

	queue, services := newFakeSQS(), &fakeServices{desired: 1}
	queue.counts = emptyQueues()
	working := make(chan struct{})
	worker := answering(t, func(*http.Request) int {
		time.Sleep(150 * time.Millisecond)
		close(working)
		return http.StatusOK
	})
	c := idleConsumption(t, queue, services, worker, resizeOnly())
	if _, err := c.engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	queue.wait("q-resize", sqsMessage("a", aws.ToString(queue.sentMessages()[0].MessageBody), 1))

	runUntil(t, c, func() bool {
		select {
		case <-working:
			return true
		default:
			return false
		}
	})

	if updated := services.updates(); len(updated) != 0 {
		t.Error("the service was set to no tasks in the middle of an attempt, which would have killed it")
	}
}

func TestAWorkerPollsOnlyTheQueuesOfTheConsumersItServes(t *testing.T) {
	t.Parallel()

	elsewhere := aTask("transcode", retrying(3), false)
	elsewhere.Declared.Consumers[0].Worker = "video"
	queue := newFakeSQS()
	c := idleConsumption(t, queue, &fakeServices{desired: 1}, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"resize": aTask("resize", retrying(3), false), "transcode": elsewhere})

	runUntil(t, c, func() bool { queue.mu.Lock(); defer queue.mu.Unlock(); return len(queue.received) >= 3 })

	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, in := range queue.received {
		if aws.ToString(in.QueueUrl) != fakeQueueHost+"q-resize" {
			t.Fatalf("the media worker polled %s, want only q-resize", aws.ToString(in.QueueUrl))
		}
	}
}

func TestAWorkerThatServesNoQueueRefusesToRun(t *testing.T) {
	t.Parallel()

	c := idleConsumption(t, newFakeSQS(), &fakeServices{}, "", map[string]queues.Topic{})
	if err := c.Run(context.Background()); err == nil {
		t.Error("Run() = nil, want an error naming the worker that serves nothing")
	}
}
