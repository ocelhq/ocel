package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type fakeServices struct {
	mu        sync.Mutex
	desired   int32
	described int
	updated   []*ecs.UpdateServiceInput
	failWith  error
}

func (f *fakeServices) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.described++
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{ServiceName: aws.String(in.Services[0]), DesiredCount: f.desired}}}, nil
}

func (f *fakeServices) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, in)
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *fakeServices) updates() []*ecs.UpdateServiceInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*ecs.UpdateServiceInput(nil), f.updated...)
}

func containerWorkers() map[string]queues.Worker {
	return map[string]queues.Worker{"media": {Service: &queues.Service{Cluster: "ocel-containers-production", Name: "ocel-app-media"}}}
}

func wakingQueue(services *fakeServices, workers map[string]queues.Worker) (*Queue, *fakeSQS) {
	topology := queues.Topology{Topics: map[string]queues.Topic{"resize": aTask("resize", retrying(3), false), "orders": aTopic("orders", false, "ship")}, Workers: workers}
	queue := newFakeSQS()
	return NewQueue(topology, queue, &fakeSNS{}, services), queue
}

func sendToResize(t *testing.T, q *Queue, due time.Duration) {
	t.Helper()
	if err := q.Send(context.Background(), runs.Message{Topic: "resize", Consumer: "resize", Execution: "e-resize", Delivery: "d", DueAt: time.Now().Add(due)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestASendToAWorkerSittingAtNoTasksStartsOneTask(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	q, _ := wakingQueue(services, containerWorkers())
	sendToResize(t, q, 0)

	if len(services.updated) != 1 {
		t.Fatalf("UpdateService called %d times, want once to wake the worker the message waits for", len(services.updated))
	}
	got := services.updated[0]
	if aws.ToString(got.Cluster) != "ocel-containers-production" || aws.ToString(got.Service) != "ocel-app-media" || aws.ToInt32(got.DesiredCount) != 1 {
		t.Errorf("UpdateService(%s, %s, desired %d), want the worker's own service at one task", aws.ToString(got.Cluster), aws.ToString(got.Service), aws.ToInt32(got.DesiredCount))
	}
}

func TestASendNeverScalesAWorkerThatIsAlreadyRunningDown(t *testing.T) {
	t.Parallel()

	services := &fakeServices{desired: 3}
	q, _ := wakingQueue(services, containerWorkers())
	sendToResize(t, q, 0)

	if len(services.updated) != 0 {
		t.Errorf("UpdateService called with desired %d on a service already at 3 tasks, want it left alone", aws.ToInt32(services.updated[0].DesiredCount))
	}
}

func TestSendsInARowReadTheServiceOnceSoABurstCostsOneDescribe(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	q, _ := wakingQueue(services, containerWorkers())
	for range 20 {
		sendToResize(t, q, 0)
	}

	if services.described != 1 || len(services.updated) != 1 {
		t.Errorf("20 sends described the service %d times and updated it %d times, want once each", services.described, len(services.updated))
	}
}

func TestAMessageNotDueForAWhileDoesNotWakeTheWorkerItsQueueDoesAtTheTime(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	q, _ := wakingQueue(services, containerWorkers())
	sendToResize(t, q, 10*time.Minute)

	if services.described != 0 || len(services.updated) != 0 {
		t.Errorf("a message due in ten minutes touched the service (%d describes, %d updates), want the worker left at zero until a backlog alarm sees it due", services.described, len(services.updated))
	}
}

func TestASendIsNotRefusedWhenTheWorkerCannotBeWoken(t *testing.T) {
	t.Parallel()

	q, queue := wakingQueue(&fakeServices{failWith: errors.New("AccessDeniedException")}, containerWorkers())
	sendToResize(t, q, 0)

	if len(queue.sentMessages()) != 1 {
		t.Error("the message was not sent, want it on its queue for the backlog alarm to start the worker")
	}
}

func TestAPublishWakesTheWorkerOfEachConsumerOfTheTopic(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	q, _ := wakingQueue(services, containerWorkers())
	if err := q.Send(context.Background(), runs.Message{Topic: "orders", ID: "m"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(services.updated) != 1 || aws.ToString(services.updated[0].Service) != "ocel-app-media" {
		t.Errorf("publish updated %+v, want the media worker woken: SNS delivers to its queue whenever it likes", services.updated)
	}
}

func TestAWorkerOnFunctionsIsNeverWokenBecauseItsQueueInvokesIt(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	q, _ := wakingQueue(services, nil)
	sendToResize(t, q, 0)

	if services.described != 0 {
		t.Error("a send read an ECS service for a worker that runs as a function")
	}
}
