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
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/ocelhq/ocel/pkg/provider"
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

type publishedMessages struct{}

func (publishedMessages) Publish(context.Context, *sns.PublishInput, ...func(*sns.Options)) (*sns.PublishOutput, error) {
	return &sns.PublishOutput{}, nil
}

func wakingEngine(services *fakeServices) (*Engine, deployedConsumer) {
	consumer := provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}
	declared := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
	e := New(Config{
		Topology: queues.Topology{
			Topics:  map[string]queues.Topic{"resize": {Declared: declared, Queues: map[string]string{"resize": "q-resize"}, SNS: "arn:aws:sns:::resize"}},
			Workers: map[string]queues.Worker{"media": {Service: &queues.Service{Cluster: "ocel-containers-production", Name: "ocel-app-media"}}},
		},
		Queues:   &sentMessages{},
		Topics:   publishedMessages{},
		Services: services,
	})
	return e, deployedConsumer{topicName: "resize", topic: declared, consumer: consumer, queue: "q-resize"}
}

func TestASendToAWorkerSittingAtNoTasksStartsOneTask(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	e, deployed := wakingEngine(services)

	if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
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
	e, deployed := wakingEngine(services)

	if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(services.updated) != 0 {
		t.Errorf("UpdateService called with desired %d on a service already at 3 tasks, want it left alone", aws.ToInt32(services.updated[0].DesiredCount))
	}
}

func TestSendsInARowReadTheServiceOnceSoABurstCostsOneDescribe(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	e, deployed := wakingEngine(services)

	for range 20 {
		if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 0); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if services.described != 1 || len(services.updated) != 1 {
		t.Errorf("20 sends described the service %d times and updated it %d times, want once each", services.described, len(services.updated))
	}
}

func TestAMessageNotDueForAWhileDoesNotWakeTheWorkerItsQueueDoesAtTheTime(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	e, deployed := wakingEngine(services)

	if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 10*time.Minute); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if services.described != 0 || len(services.updated) != 0 {
		t.Errorf("a message due in ten minutes touched the service (%d describes, %d updates), want the worker left at zero until a backlog alarm sees it due", services.described, len(services.updated))
	}
}

func TestASendIsNotRefusedWhenTheWorkerCannotBeWoken(t *testing.T) {
	t.Parallel()

	services := &fakeServices{failWith: errors.New("AccessDeniedException")}
	e, deployed := wakingEngine(services)

	if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 0); err != nil {
		t.Errorf("enqueue = %v, want the message sent: it waits on its queue and the backlog alarm starts the worker", err)
	}
}

func TestAPublishWakesTheWorkerOfEachConsumerOfTheTopic(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	e, _ := wakingEngine(services)

	if err := e.publish(context.Background(), "resize", "arn:aws:sns:::resize", false, queueMessage{Message: "m"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(services.updated) != 1 || aws.ToString(services.updated[0].Service) != "ocel-app-media" {
		t.Errorf("publish updated %+v, want the media worker woken: SNS delivers to its queue whenever it likes", services.updated)
	}
}

func TestAWorkerOnFunctionsIsNeverWokenBecauseItsQueueInvokesIt(t *testing.T) {
	t.Parallel()

	services := &fakeServices{}
	e, deployed := wakingEngine(services)
	e.cfg.Topology.Workers = nil

	if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e", Delivery: "d"}, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if services.described != 0 {
		t.Error("a send read an ECS service for a worker that runs as a function")
	}
}
