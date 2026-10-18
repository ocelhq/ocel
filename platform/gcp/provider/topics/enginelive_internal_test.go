//go:build integration

package topics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/api/iterator"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	emulatorVariable = "OCEL_FLOCI_GCP_ENDPOINT"
	pullAckDeadline  = 10 * time.Second
	pullPoll         = 50 * time.Millisecond
	dispatchPoll     = 100 * time.Millisecond
)

func emulatorClients(t *testing.T) *ports.Clients {
	t.Helper()
	endpoint := os.Getenv(emulatorVariable)
	if endpoint == "" {
		t.Fatalf("no floci-gcp emulator in the environment; run under `scripts/floci.sh --cloud gcp run <name> -- go test -tags integration ./...`")
	}
	return &ports.Clients{Namespace: "ocel", Project: "floci-local", Region: "europe-west1", Endpoint: endpoint}
}

func emulatedScope(t *testing.T) Scope {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return Scope{Slug: "shop", Tier: environment.TierPreview, Environment: "t" + hex.EncodeToString(sum[:6])}
}

type pullReceiver struct {
	service *pubsub.Service
	project string
	names   Names
}

func (p pullReceiver) subscription(topic, consumer string) string {
	return subscriptionPath(p.project, p.names.Subscription(topic, consumer))
}

func (p pullReceiver) Receive(ctx context.Context, req runs.ReceiveRequest) ([]runs.Received, error) {
	deadline := time.Now().Add(req.Wait)
	subscription := p.subscription(req.Topic, req.Consumer)
	for {
		pulled, err := p.service.Projects.Subscriptions.Pull(subscription, &pubsub.PullRequest{MaxMessages: int64(req.Max), ReturnImmediately: true}).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		if len(pulled.ReceivedMessages) > 0 {
			received := make([]runs.Received, 0, len(pulled.ReceivedMessages))
			handles := make([]string, 0, len(pulled.ReceivedMessages))
			for _, message := range pulled.ReceivedMessages {
				msg, err := messageOf(message.Message, time.Now())
				if err != nil {
					return nil, err
				}
				received = append(received, runs.Received{Handle: message.AckId, Message: msg, Receives: int(message.DeliveryAttempt)})
				handles = append(handles, message.AckId)
			}
			return received, p.Extend(ctx, req.Topic, req.Consumer, handles, req.Hidden)
		}
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(pullPoll, time.Until(deadline))):
		}
	}
}

func ackDeadlineOf(hidden time.Duration) int64 {
	return int64(math.Ceil(min(hidden, pushAckDeadline).Seconds()))
}

func (p pullReceiver) Extend(ctx context.Context, topic, consumer string, handles []string, hidden time.Duration) error {
	if len(handles) == 0 {
		return nil
	}
	_, err := p.service.Projects.Subscriptions.ModifyAckDeadline(p.subscription(topic, consumer), &pubsub.ModifyAckDeadlineRequest{
		AckIds: handles, AckDeadlineSeconds: ackDeadlineOf(hidden), ForceSendFields: []string{"AckDeadlineSeconds"},
	}).Context(ctx).Do()
	return err
}

func (p pullReceiver) Ack(ctx context.Context, topic, consumer string, handles []string) error {
	_, err := p.service.Projects.Subscriptions.Acknowledge(p.subscription(topic, consumer), &pubsub.AcknowledgeRequest{AckIds: handles}).Context(ctx).Do()
	return err
}

func (p pullReceiver) Hide(ctx context.Context, topic, consumer, handle string, hidden time.Duration) error {
	return p.Extend(ctx, topic, consumer, []string{handle}, hidden)
}

func pulledTopology(t *testing.T, clients *ports.Clients, names Names, declared map[string]*provider.TopicSpec) {
	t.Helper()
	ctx := context.Background()
	topology := Topology{Names: names, Topics: declared}
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })
	if err := topology.Ensure(ctx, clients); err != nil {
		t.Fatalf("Topology.Ensure() = %v", err)
	}
	service, err := clients.PubSub()
	if err != nil {
		t.Fatal(err)
	}
	pulled := Subscriptions{Names: names, Topics: declared}
	for name, topic := range declared {
		for _, consumer := range topic.Consumers {
			subscription := pulled.subscription(clients.Project, name, topic, consumer, Push{})
			subscription.PushConfig, subscription.AckDeadlineSeconds = nil, int64(pullAckDeadline/time.Second)
			if err := ensureSubscription(ctx, service, subscriptionPath(clients.Project, names.Subscription(name, consumer.Name)), subscription); err != nil {
				t.Fatalf("make the pull subscription of %s/%s: %v", name, consumer.Name, err)
			}
		}
	}
}

func dispatchDueTasks(t *testing.T, clients *ports.Clients, delays Delays) {
	t.Helper()
	client, err := clients.CloudTasks()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	var dispatching sync.WaitGroup
	t.Cleanup(func() {
		stop()
		dispatching.Wait()
	})
	dispatching.Go(func() {
		ticker := time.NewTicker(dispatchPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			listed := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: delays.Queue, ResponseView: cloudtaskspb.Task_FULL})
			for {
				task, err := listed.Next()
				if errors.Is(err, iterator.Done) || err != nil {
					break
				}
				if task.GetScheduleTime().AsTime().After(time.Now()) {
					continue
				}
				request := task.GetHttpRequest()
				resp, err := http.Post(request.GetUrl(), "application/json", bytes.NewReader(request.GetBody()))
				if err != nil {
					continue
				}
				_ = resp.Body.Close()
				_ = client.DeleteTask(ctx, &cloudtaskspb.DeleteTaskRequest{Name: task.GetName()})
			}
		}
	})
}

func TestLiveFirestoreAndPubSubKeepEveryRunSemanticOfTheEngine(t *testing.T) {
	runsconformance.Engine(t, func(t *testing.T, declared map[string]*provider.TopicSpec) runsconformance.Wiring {
		clients := emulatorClients(t)
		scope := emulatedScope(t)
		names := Names{Namespace: clients.Namespace, Scope: scope}
		delays := Delays{
			Queue:      "projects/" + clients.Project + "/locations/" + clients.Region + "/queues/" + strings.ReplaceAll(scope.Environment, ".", "-"),
			Account:    "ocel-preview@floci-local.iam.gserviceaccount.com",
			PublishURL: clients.Endpoint,
		}
		if err := delays.Ensure(context.Background(), clients); err != nil {
			t.Fatalf("Delays.Ensure() = %v", err)
		}
		pulledTopology(t, clients, names, declared)
		dispatchDueTasks(t, clients, delays)
		service, err := clients.PubSub()
		if err != nil {
			t.Fatal(err)
		}
		deployment := Deployment{Clients: clients, Names: names, Declared: declared, Delays: delays}
		return runsconformance.Wiring{
			Store:    deployment.Store(),
			Queue:    deployment.Queue(),
			Receiver: pullReceiver{service: service, project: clients.Project, names: names},
		}
	})
}
