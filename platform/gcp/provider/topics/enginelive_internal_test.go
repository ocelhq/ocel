//go:build integration

package topics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"slices"
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

// HACK: floci-gcp hands a pulled message out once, never again past its ack deadline, and
// answers modifyAckDeadline with a 404, so this receiver keeps each pulled message's lease
// itself, as Pub/Sub keeps it; pull and lease through Pub/Sub alone once floci-gcp
// redelivers and serves modifyAckDeadline.
type pullReceiver struct {
	service *pubsub.Service
	project string
	names   Names
	topics  map[string]*provider.TopicSpec

	mu   sync.Mutex
	held map[string][]*leased
}

type leased struct {
	handle    string
	message   runs.Message
	receives  int
	visibleAt time.Time
}

func (p *pullReceiver) subscription(topic, consumer string) string {
	return subscriptionPath(p.project, p.names.Subscription(topic, consumer))
}

func (p *pullReceiver) Receive(ctx context.Context, req runs.ReceiveRequest) ([]runs.Received, error) {
	deadline := time.Now().Add(req.Wait)
	subscription := p.subscription(req.Topic, req.Consumer)
	for {
		pulled, err := p.service.Projects.Subscriptions.Pull(subscription, &pubsub.PullRequest{MaxMessages: int64(req.Max), ReturnImmediately: true}).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		if err := p.hold(subscription, pulled.ReceivedMessages); err != nil {
			return nil, err
		}
		if received := p.take(subscription, req); len(received) > 0 {
			return received, nil
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

func (p *pullReceiver) hold(subscription string, pulled []*pubsub.ReceivedMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, each := range pulled {
		msg, err := messageOf(each.Message, time.Now())
		if err != nil {
			return err
		}
		p.held[subscription] = append(p.held[subscription], &leased{handle: each.AckId, message: msg})
	}
	return nil
}

func (p *pullReceiver) take(subscription string, req runs.ReceiveRequest) []runs.Received {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	ordered := p.topics[req.Topic] != nil && p.topics[req.Topic].Ordered
	blocked := map[string]bool{}
	var received []runs.Received
	for _, each := range p.held[subscription] {
		if len(received) == req.Max {
			break
		}
		key := each.message.Key
		if ordered && key != "" && blocked[key] {
			continue
		}
		if each.visibleAt.After(now) {
			blocked[key] = true
			continue
		}
		each.receives++
		each.visibleAt = now.Add(req.Hidden)
		received = append(received, runs.Received{Handle: each.handle, Message: each.message, Receives: each.receives})
	}
	return received
}

func (p *pullReceiver) lease(subscription string, handles []string, visibleAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, each := range p.held[subscription] {
		if slices.Contains(handles, each.handle) {
			each.visibleAt = visibleAt
		}
	}
}

func (p *pullReceiver) Extend(_ context.Context, topic, consumer string, handles []string, hidden time.Duration) error {
	p.lease(p.subscription(topic, consumer), handles, time.Now().Add(hidden))
	return nil
}

func (p *pullReceiver) Ack(ctx context.Context, topic, consumer string, handles []string) error {
	subscription := p.subscription(topic, consumer)
	if _, err := p.service.Projects.Subscriptions.Acknowledge(subscription, &pubsub.AcknowledgeRequest{AckIds: handles}).Context(ctx).Do(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held[subscription] = slices.DeleteFunc(p.held[subscription], func(each *leased) bool { return slices.Contains(handles, each.handle) })
	return nil
}

func (p *pullReceiver) Hide(ctx context.Context, topic, consumer, handle string, hidden time.Duration) error {
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
			Receiver: &pullReceiver{service: service, project: clients.Project, names: names, topics: declared, held: map[string][]*leased{}},
		}
	})
}
