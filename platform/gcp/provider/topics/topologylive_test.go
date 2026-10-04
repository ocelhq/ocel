//go:build integration

package topics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

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

const (
	appsMember = "serviceAccount:" + invoker
	agent      = "serviceAccount:service-1@gcp-sa-pubsub.iam.gserviceaccount.com"
)

func deployedTopology(t *testing.T, clients *ports.Clients, names topics.Names, declared map[string]*provider.TopicSpec) (topics.Topology, topics.Subscriptions) {
	t.Helper()
	topology := topics.Topology{Names: names, Topics: declared, Publisher: appsMember, Agent: agent}
	subscriptions := topics.Subscriptions{Names: names, Topics: declared, Pushes: pushesTo("https://worker.run.app"), Agent: agent}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Topology.Ensure() = %v", err)
	}
	if err := subscriptions.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Subscriptions.Ensure() = %v", err)
	}
	return topology, subscriptions
}

type policyShape struct {
	Bindings []struct {
		Role    string   `json:"role"`
		Members []string `json:"members"`
	} `json:"bindings"`
}

func readPolicy(t *testing.T, endpoint, project, kind, name string) policyShape {
	t.Helper()
	resp, err := http.Get(endpoint + "/v1/projects/" + project + "/" + kind + "/" + name + ":getIamPolicy")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var policy policyShape
	_ = json.NewDecoder(resp.Body).Decode(&policy)
	return policy
}

func (p policyShape) grants(role, member string) bool {
	for _, binding := range p.Bindings {
		if binding.Role == role && slices.Contains(binding.Members, member) {
			return true
		}
	}
	return false
}

func TestLiveTopologyGivesEachConsumerASubscriptionAndDeadLetterTopicOfItsOwn(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology, subscriptions := deployedTopology(t, clients, names, ordersAndResize())
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Topology.Ensure() over the topology it made = %v, want it kept", err)
	}
	if err := subscriptions.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Subscriptions.Ensure() over the subscriptions it made = %v, want them kept", err)
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

func TestLiveTheAppsMayPublishToEachTopicAndPubSubMayDeadLetterEachConsumer(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology, _ := deployedTopology(t, clients, names, ordersAndResize())
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	for _, topic := range []string{"orders", "resize"} {
		if !readPolicy(t, endpoint, clients.Project, "topics", names.Topic(topic)).grants("roles/pubsub.publisher", appsMember) {
			t.Errorf("%s may not publish to %s, and every app and worker in the tier sends and triggers as it", appsMember, topic)
		}
	}
	for _, consumer := range []struct{ topic, consumer string }{{"orders", "ship"}, {"orders", "bill"}, {"resize", "resize"}} {
		if !readPolicy(t, endpoint, clients.Project, "topics", names.DeadLetterTopic(consumer.topic, consumer.consumer)).grants("roles/pubsub.publisher", agent) {
			t.Errorf("Pub/Sub may not publish to the dead-letter topic of %s/%s, so it could never dead-letter a message", consumer.topic, consumer.consumer)
		}
		if !readPolicy(t, endpoint, clients.Project, "subscriptions", names.Subscription(consumer.topic, consumer.consumer)).grants("roles/pubsub.subscriber", agent) {
			t.Errorf("Pub/Sub may not acknowledge on %s/%s, so a dead-lettered message would stay on the subscription", consumer.topic, consumer.consumer)
		}
	}
}

func TestLiveSubscriptionsAreMadeOnlyForTheWorkersAnAppHosts(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology := topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: appsMember, Agent: agent}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	billing := topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: map[string]topics.Push{"billing": pushesTo("https://worker.run.app")["billing"]}, Agent: agent}
	if err := billing.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() for the app hosting billing alone = %v, want the consumers on other workers left to their apps", err)
	}
	if _, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "bill")); status != http.StatusOK {
		t.Errorf("orders/bill, on billing, answered %d, want it made", status)
	}
	if _, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship")); status != http.StatusNotFound {
		t.Errorf("orders/ship, on another app's worker, answered %d, want it left alone", status)
	}
}

func TestLiveRemovingAConsumerTakesItsSubscriptionAndDeadLetterTopicAndNoOther(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology, _ := deployedTopology(t, clients, names, ordersAndResize())
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	previous := ordersAndResize()
	shipping := ordersAndResize()
	shipping["orders"].Consumers = shipping["orders"].Consumers[:1]
	topology.Topics = shipping
	if err := topology.RemoveDropped(context.Background(), clients, previous); err != nil {
		t.Fatalf("RemoveDropped() after bill stopped consuming orders = %v", err)
	}
	if _, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "bill")); status != http.StatusNotFound {
		t.Errorf("orders/bill answered %d, want it gone", status)
	}
	if status := readTopicStatus(t, endpoint, clients.Project, names.DeadLetterTopic("orders", "bill")); status != http.StatusNotFound {
		t.Errorf("the dead-letter topic of orders/bill answered %d, want it gone", status)
	}
	if _, status := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship")); status != http.StatusOK {
		t.Errorf("orders/ship answered %d, want it kept", status)
	}
	if err := topology.RemoveDropped(context.Background(), clients, previous); err != nil {
		t.Errorf("RemoveDropped() of a consumer already gone = %v, want nothing to do", err)
	}
	topology.Topics = ordersAndResize()
}

func TestLiveATopicWhoseOrderingChangedIsRefusedAndItsSubscriptionsKept(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	declared := ordersAndResize()
	topology, subscriptions := deployedTopology(t, clients, names, declared)
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	declared["orders"].Ordered = false
	err := subscriptions.Ensure(context.Background(), clients)
	if err == nil || !strings.Contains(err.Error(), "ship") {
		t.Fatalf("Ensure() after orders stopped being ordered = %v, want it refused naming the subscription: Pub/Sub cannot change a subscription's ordering", err)
	}
	if ship, _ := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship")); !ship.EnableOrdering {
		t.Error("the refused Ensure() changed orders/ship's ordering anyway")
	}
}

func TestLiveFlociDropsAPushSubscriptionsOIDCToken(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology, _ := deployedTopology(t, clients, names, ordersAndResize())
	t.Cleanup(func() { _ = topology.Remove(context.Background(), clients) })

	ship, _ := readSubscription(t, endpoint, clients.Project, names.Subscription("orders", "ship"))
	if ship.PushConfig.OidcToken != nil {
		t.Fatalf("floci now keeps a push subscription's oidcToken (%+v): read it back in the topology test and drop this guard", ship.PushConfig.OidcToken)
	}
}
