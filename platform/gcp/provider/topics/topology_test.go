package topics_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const invoker = "ocel-production@floci-local.iam.gserviceaccount.com"

func ordersAndResize() map[string]*contractv1.ManifestTopic {
	return map[string]*contractv1.ManifestTopic{
		"orders": {
			Ordered: true,
			Retry:   &resourcesv1.RetryPolicy{MaxAttempts: 3, MinDelay: durationpb.New(2 * time.Second), MaxDelay: durationpb.New(90 * time.Second)},
			Consumers: []*contractv1.ManifestConsumer{
				{Name: "ship", Worker: "worker"},
				{Name: "bill", Worker: "billing", Retry: &resourcesv1.RetryPolicy{MinDelay: durationpb.New(5 * time.Second)}},
			},
		},
		"resize": {Consumers: []*contractv1.ManifestConsumer{{Name: "resize", Worker: "worker", Exclusive: true}}},
	}
}

func pushesTo(base string) map[string]topics.Push {
	return map[string]topics.Push{
		"worker":  {URL: base + "/worker", ServiceAccount: invoker},
		"billing": {URL: base + "/billing", ServiceAccount: invoker},
	}
}

type subscriptionShape struct {
	Topic              string `json:"topic"`
	AckDeadlineSeconds int    `json:"ackDeadlineSeconds"`
	EnableOrdering     bool   `json:"enableMessageOrdering"`
	Filter             string `json:"filter"`
	PushConfig         struct {
		PushEndpoint string `json:"pushEndpoint"`
		OidcToken    *struct {
			ServiceAccountEmail string `json:"serviceAccountEmail"`
			Audience            string `json:"audience"`
		} `json:"oidcToken"`
	} `json:"pushConfig"`
	RetryPolicy struct {
		MinimumBackoff string `json:"minimumBackoff"`
		MaximumBackoff string `json:"maximumBackoff"`
	} `json:"retryPolicy"`
	DeadLetterPolicy struct {
		DeadLetterTopic     string `json:"deadLetterTopic"`
		MaxDeliveryAttempts int    `json:"maxDeliveryAttempts"`
	} `json:"deadLetterPolicy"`
}

func readSubscription(t *testing.T, endpoint, project, name string) (subscriptionShape, int) {
	t.Helper()
	resp, err := http.Get(endpoint + "/v1/projects/" + project + "/subscriptions/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var shape subscriptionShape
	_ = json.NewDecoder(resp.Body).Decode(&shape)
	return shape, resp.StatusCode
}

func readTopicStatus(t *testing.T, endpoint, project, name string) int {
	t.Helper()
	resp, err := http.Get(endpoint + "/v1/projects/" + project + "/topics/" + name)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func emulatedEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv(emulatorEndpointVariable)
	if endpoint == "" {
		t.Skip("no floci-gcp emulator in the environment: this reads Pub/Sub back over the emulator's REST surface")
	}
	return endpoint
}

func TestLiveTopologyGivesEachConsumerASubscriptionAndDeadLetterTopicOfItsOwn(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology := topics.Topology{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app")}

	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() over the topology it made = %v, want it kept", err)
	}

	for _, topic := range []string{names.Topic("orders"), names.Topic("resize"), names.DeadLetterTopic("orders", "ship"), names.DeadLetterTopic("orders", "bill"), names.DeadLetterTopic("resize", "resize")} {
		if status := readTopicStatus(t, endpoint, clients.Project, topic); status != http.StatusOK {
			t.Errorf("topic %s answered %d, want it made", topic, status)
		}
	}

	ship, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship"))
	if status != http.StatusOK {
		t.Fatalf("the subscription of orders/ship answered %d, want it made", status)
	}
	if ship.Topic != "projects/"+clients.Project+"/topics/"+names.Topic("orders") {
		t.Errorf("orders/ship subscribes %s, want the orders topic", ship.Topic)
	}
	if want := "https://worker.run.app/worker/topics/orders/consumers/ship"; ship.PushConfig.PushEndpoint != want {
		t.Errorf("orders/ship pushes to %s, want %s: the worker reads the declared topic and consumer off the path", ship.PushConfig.PushEndpoint, want)
	}
	if !ship.EnableOrdering {
		t.Error("orders/ship delivers out of order, and orders is an ordered topic")
	}
	if ship.AckDeadlineSeconds != 600 {
		t.Errorf("orders/ship gives an attempt %ds, want the 600s a push may take", ship.AckDeadlineSeconds)
	}
	if ship.RetryPolicy.MinimumBackoff != "2s" || ship.RetryPolicy.MaximumBackoff != "90s" {
		t.Errorf("orders/ship backs off %s to %s, want the topic's 2s to 90s", ship.RetryPolicy.MinimumBackoff, ship.RetryPolicy.MaximumBackoff)
	}
	if ship.DeadLetterPolicy.DeadLetterTopic != "projects/"+clients.Project+"/topics/"+names.DeadLetterTopic("orders", "ship") {
		t.Errorf("orders/ship dead-letters to %s, want its own dead-letter topic", ship.DeadLetterPolicy.DeadLetterTopic)
	}
	if !strings.Contains(ship.Filter, `"ship"`) {
		t.Errorf("orders/ship filters on %q, want a message redriven to one consumer to reach that consumer alone", ship.Filter)
	}

	bill, _ := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "bill"))
	if want := "https://worker.run.app/billing/topics/orders/consumers/bill"; bill.PushConfig.PushEndpoint != want {
		t.Errorf("orders/bill pushes to %s, want %s on the worker it is placed on", bill.PushConfig.PushEndpoint, want)
	}
	if bill.RetryPolicy.MinimumBackoff != "5s" || bill.RetryPolicy.MaximumBackoff != "90s" {
		t.Errorf("orders/bill backs off %s to %s, want its own 5s over the topic's 90s", bill.RetryPolicy.MinimumBackoff, bill.RetryPolicy.MaximumBackoff)
	}

	if err := topology.Remove(context.Background(), clients); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if _, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship")); status != http.StatusNotFound {
		t.Errorf("the subscription of orders/ship answered %d after Remove(), want it gone", status)
	}
	for _, topic := range []string{names.Topic("orders"), names.DeadLetterTopic("orders", "ship")} {
		if status := readTopicStatus(t, endpoint, clients.Project, topic); status != http.StatusNotFound {
			t.Errorf("topic %s answered %d after Remove(), want it gone", topic, status)
		}
	}
	if err := topology.Remove(context.Background(), clients); err != nil {
		t.Errorf("Remove() of a topology already gone = %v, want nothing to do", err)
	}
}

func TestLiveFlociDropsAPushSubscriptionsOIDCToken(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology := topics.Topology{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app")}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	ship, _ := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship"))
	if ship.PushConfig.OidcToken != nil {
		t.Fatalf("floci now keeps a push subscription's oidcToken (%+v): read it back in the topology test and drop this guard", ship.PushConfig.OidcToken)
	}
}

type capturedPubSub struct {
	mu            sync.Mutex
	subscriptions map[string]subscriptionShape
}

func (c *capturedPubSub) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/subscriptions/") {
		var shape subscriptionShape
		_ = json.Unmarshal(body, &shape)
		c.subscriptions[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]] = shape
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func TestEveryPushIsSignedAsTheInvokerForTheWorkerItReaches(t *testing.T) {
	captured := &capturedPubSub{subscriptions: map[string]subscriptionShape{}}
	server := httptest.NewServer(http.HandlerFunc(captured.serve))
	t.Cleanup(server.Close)
	clients := &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL}
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}

	if err := (topics.Topology{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app")}).Ensure(context.Background(), clients); err != nil {
		t.Fatal(err)
	}

	for _, consumer := range []struct{ topic, consumer, worker string }{{"orders", "ship", "worker"}, {"orders", "bill", "billing"}, {"resize", "resize", "worker"}} {
		shape, made := captured.subscriptions[names.Subscription(consumer.topic, consumer.consumer)]
		if !made {
			t.Errorf("no subscription was made for %s/%s", consumer.topic, consumer.consumer)
			continue
		}
		token := shape.PushConfig.OidcToken
		if token == nil || token.ServiceAccountEmail != invoker || token.Audience != "https://worker.run.app/"+consumer.worker {
			t.Errorf("%s/%s pushes with token %+v, want one minted for %s with the worker's url as audience: an internal Cloud Run service admits no unsigned push",
				consumer.topic, consumer.consumer, token, invoker)
		}
	}
}
