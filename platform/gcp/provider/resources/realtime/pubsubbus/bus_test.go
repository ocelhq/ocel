package pubsubbus_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime/pubsubbus"
)

const topic = "projects/acme-prod/topics/ocel-shop-prod-realtime-abc123"

type heard struct {
	channel, event string
}

func newPubSub(t *testing.T) (*googletest.PubSub, []option.ClientOption) {
	t.Helper()
	fake := googletest.NewPubSub()
	server := httptest.NewServer(fake.Serve(t))
	t.Cleanup(server.Close)
	fake.AddTopic(topic)
	return fake, []option.ClientOption{option.WithEndpoint(server.URL), option.WithoutAuthentication()}
}

func openBus(t *testing.T, options []option.ClientOption) *pubsubbus.Bus {
	t.Helper()
	bus, err := pubsubbus.Open(context.Background(), topic, options...)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	return bus
}

func listen(t *testing.T, bus *pubsubbus.Bus) <-chan heard {
	t.Helper()
	got := make(chan heard, 16)
	stop := bus.Listen(func(channel, event string) { got <- heard{channel, event} })
	t.Cleanup(stop)
	return got
}

func awaitHeard(t *testing.T, got <-chan heard, who string, want heard) {
	t.Helper()
	select {
	case message := <-got:
		if message != want {
			t.Errorf("%s heard %+v, want %+v", who, message, want)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("%s never heard %+v", who, want)
	}
}

func TestEveryBusOnATopicHearsEveryPublishAnyOfThemMakes(t *testing.T) {
	t.Parallel()

	_, options := newPubSub(t)
	first, second := openBus(t, options), openBus(t, options)
	firstHeard, secondHeard := listen(t, first), listen(t, second)

	for _, publisher := range []*pubsubbus.Bus{first, second} {
		want := heard{channel: "/app/orders/o-1", event: `{"v":1,"data":{"status":"shipped"}}`}
		if err := publisher.Publish(context.Background(), want.channel, want.event); err != nil {
			t.Fatalf("Publish() = %v", err)
		}
		awaitHeard(t, firstHeard, "the first instance", want)
		awaitHeard(t, secondHeard, "the second instance", want)
	}
}

func TestEachBusSubscribesForItselfAndASubscriptionLeftBehindLapsesInADay(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	openBus(t, options)
	openBus(t, options)

	subscriptions := fake.SubscriptionsOf(topic)
	if len(subscriptions) != 2 || subscriptions[0].Name == subscriptions[1].Name {
		t.Fatalf("the topic has %d subscriptions, want one for each bus: instances sharing a subscription split its messages", len(subscriptions))
	}
	for _, subscription := range subscriptions {
		if subscription.ExpirationPolicy == nil || subscription.ExpirationPolicy.Ttl != "86400s" {
			t.Errorf("subscription %s expires on %+v, want 86400s, the shortest Pub/Sub allows, so a killed instance's subscription is reclaimed", subscription.Name, subscription.ExpirationPolicy)
		}
		if subscription.MessageRetentionDuration != "600s" {
			t.Errorf("subscription %s retains messages for %q, want 600s: a live event nobody pulled in ten minutes is stale", subscription.Name, subscription.MessageRetentionDuration)
		}
		if subscription.AckDeadlineSeconds != 10 {
			t.Errorf("subscription %s waits %ds for an ack, want 10", subscription.Name, subscription.AckDeadlineSeconds)
		}
	}
}

func TestClosingABusDeletesItsSubscription(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	bus, err := pubsubbus.Open(context.Background(), topic, options...)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	listen(t, bus)
	if err := bus.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if left := fake.SubscriptionsOf(topic); len(left) != 0 {
		t.Errorf("the topic keeps %d subscriptions once its only bus closed, want none", len(left))
	}
}

func TestABusAcknowledgesWhatItDelivers(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	bus := openBus(t, options)
	got := listen(t, bus)
	if err := bus.Publish(context.Background(), "/app/a", "{}"); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	awaitHeard(t, got, "the bus", heard{channel: "/app/a", event: "{}"})
	if err := bus.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if acked := fake.Acknowledged(); acked != 1 {
		t.Errorf("%d messages were acknowledged, want 1: an unacknowledged message is delivered again ten seconds later", acked)
	}
}

func TestABusWhoseSubscriptionWasDeletedSubscribesAgain(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	bus := openBus(t, options)
	got := listen(t, bus)
	fake.DropSubscription(fake.SubscriptionsOf(topic)[0].Name)

	deadline := time.After(5 * time.Second)
	for {
		if err := bus.Publish(context.Background(), "/app/a", "{}"); err != nil {
			t.Fatalf("Publish() = %v", err)
		}
		select {
		case <-got:
			return
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("a bus whose subscription was deleted never heard a publish again, and its instance's sockets would miss every event until it restarts")
		}
	}
}

func TestABusWaitsOutAGrantGoogleHasNotYetMadeVisible(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	fake.Mu.Lock()
	fake.DeniedCreates = 2
	fake.Mu.Unlock()
	openBus(t, options)

	if got := fake.SubscriptionsOf(topic); len(got) != 1 {
		t.Errorf("the topic has %d subscriptions, want the one made once the grant a deploy wrote on the topic took effect", len(got))
	}
}

func TestABusOnATopicThatDoesNotExistIsRefused(t *testing.T) {
	t.Parallel()

	fake, options := newPubSub(t)
	delete(fake.Topics, topic)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if bus, err := pubsubbus.Open(ctx, topic, options...); err == nil {
		_ = bus.Close(context.Background())
		t.Error("Open() = nil, want an error: a gateway subscribed to nothing delivers nothing")
	}
}
