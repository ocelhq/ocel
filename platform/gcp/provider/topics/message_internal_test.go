package topics

import (
	"encoding/json"
	"testing"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

func everyField() runs.Message {
	at := time.Date(2026, 10, 2, 9, 5, 0, 123_456_789, time.UTC)
	return runs.Message{
		Topic:       "orders",
		Consumer:    "ship",
		Execution:   "01K0000000000000000000000A-ship",
		Delivery:    "01K0000000000000000000000D",
		ID:          "01K0000000000000000000000A",
		PublishedAt: at,
		DueAt:       at.Add(time.Hour),
		Key:         "account-1",
		Lane:        provider.LaneHigh,
		MaxAttempts: 4,
		Payload:     json.RawMessage(exactJSON),
	}
}

func received(sent *pubsub.PubsubMessage, publishTime time.Time) *pubsub.PubsubMessage {
	arrived := *sent
	arrived.MessageId = "17"
	arrived.PublishTime = publishTime.UTC().Format(time.RFC3339Nano)
	return &arrived
}

func TestAMessageArrivesWithEveryFieldItWasSentWith(t *testing.T) {
	sent := everyField()
	got, err := messageOf(received(pubsubMessageOf(sent, true), time.Now()), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := sent
	want.Topic = ""
	if string(got.Payload) != exactJSON {
		t.Errorf("the message arrived carrying %s, want %s byte for byte", got.Payload, exactJSON)
	}
	got.Payload, want.Payload = nil, nil
	if !got.PublishedAt.Equal(want.PublishedAt) || !got.DueAt.Equal(want.DueAt) {
		t.Errorf("the message arrived published %v due %v, want %v due %v", got.PublishedAt, got.DueAt, want.PublishedAt, want.DueAt)
	}
	got.PublishedAt, got.DueAt, want.PublishedAt, want.DueAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if got.Consumer != want.Consumer || got.Execution != want.Execution || got.Delivery != want.Delivery || got.ID != want.ID ||
		got.Key != want.Key || got.Lane != want.Lane || got.MaxAttempts != want.MaxAttempts {
		t.Errorf("the message arrived as %+v, want %+v", got, want)
	}
}

func TestOnlyAnOrderedTopicsMessageCarriesItsKeyAsTheOrderingKey(t *testing.T) {
	sent := everyField()
	if key := pubsubMessageOf(sent, true).OrderingKey; key != "account-1" {
		t.Errorf("an ordered topic's message is ordered by %q, want its key", key)
	}
	if key := pubsubMessageOf(sent, false).OrderingKey; key != "" {
		t.Errorf("an unordered topic's message is ordered by %q, want none: Pub/Sub refuses an ordering key on a topic no ordered subscription reads", key)
	}
}

func TestAPublishToEveryConsumerNamesNoConsumerExecutionOrDelivery(t *testing.T) {
	sent := everyField()
	sent.Consumer, sent.Execution, sent.Delivery, sent.MaxAttempts, sent.Lane = "", "", "", 0, ""
	for _, name := range []string{ConsumerAttribute, ExecutionAttribute, DeliveryAttribute, MaxAttemptsAttribute, LaneAttribute} {
		if value, named := pubsubMessageOf(sent, false).Attributes[name]; named {
			t.Errorf("a publish to every consumer carries %s=%q, want it left out so every subscription's filter takes it", name, value)
		}
	}
}

func TestAMessageWithNoDueTimeIsDueWhenItWasPublished(t *testing.T) {
	publishTime := time.Date(2026, 10, 2, 9, 5, 0, 0, time.UTC)
	got, err := messageOf(&pubsub.PubsubMessage{MessageId: "17", PublishTime: publishTime.Format(time.RFC3339Nano)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.PublishedAt.Equal(publishTime) || !got.DueAt.Equal(publishTime) || got.ID != runs.MessageIDFrom(publishTime, "17") {
		t.Errorf("a bare message arrived as id %s published %v due %v, want an id drawn from its Pub/Sub id, published and due at %v", got.ID, got.PublishedAt, got.DueAt, publishTime)
	}
}

func TestAScheduledMessageCarriesTheMinuteItFiredAt(t *testing.T) {
	fired := time.Date(2026, 10, 2, 9, 5, 0, 412_000_000, time.UTC)
	got, err := messageOf(&pubsub.PubsubMessage{
		MessageId: "17", PublishTime: fired.Format(time.RFC3339Nano), Data: "e30=", Attributes: map[string]string{ScheduleAttribute: "true"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(runs.CronPayload(fired)) || got.Execution != "" {
		t.Errorf("a scheduled message arrived carrying %s for execution %q, want %s and no execution so its run is made on arrival", got.Payload, got.Execution, runs.CronPayload(fired))
	}
}
