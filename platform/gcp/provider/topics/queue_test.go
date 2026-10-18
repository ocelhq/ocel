package topics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type publishedTo struct {
	topic   string
	message *pubsub.PubsubMessage
}

type capturedPublishes struct {
	mu        sync.Mutex
	published []publishedTo
}

func (c *capturedPublishes) serve(w http.ResponseWriter, r *http.Request) {
	var req pubsub.PublishRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	topic, _ := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":publish")
	c.mu.Lock()
	for _, message := range req.Messages {
		c.published = append(c.published, publishedTo{topic: topic, message: message})
	}
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"messageIds":["17"]}`))
}

func newQueue(t *testing.T) (topics.Queue, *capturedPublishes) {
	t.Helper()
	captured := &capturedPublishes{}
	server := httptest.NewServer(http.HandlerFunc(captured.serve))
	t.Cleanup(server.Close)
	clients := &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL}
	return topics.Queue{Clients: clients, Names: topics.Names{Namespace: "ocel", Scope: scopeOf(t)}, Topics: deployedTopics()}, captured
}

func TestAMessageDueNowIsPublishedToItsTopicOrderedByItsKeyForItsConsumerAlone(t *testing.T) {
	queue, captured := newQueue(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	sent := runs.Message{
		Topic: "orders", Consumer: "ship", Execution: "01K0000000000000000000000A-ship", Delivery: "01K0000000000000000000000D",
		ID: "01K0000000000000000000000A", PublishedAt: now, DueAt: now, Key: "account-1", Lane: provider.LaneHigh, MaxAttempts: 2,
		Payload: json.RawMessage(exactJSON),
	}

	if err := queue.Send(context.Background(), sent); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	if len(captured.published) != 1 {
		t.Fatalf("Send() published %d messages, want 1", len(captured.published))
	}
	published := captured.published[0]
	if want := topics.TopicPath("acme-prod", queue.Names.Topic("orders")); published.topic != want {
		t.Errorf("Send() published to %s, want %s", published.topic, want)
	}
	if published.message.OrderingKey != "account-1" || published.message.Attributes[topics.ConsumerAttribute] != "ship" {
		t.Errorf("Send() published %+v, want it ordered by its key and filtered to its consumer", published.message)
	}
}

func TestAMessageToATopicNoOneDeployedIsRefused(t *testing.T) {
	queue, captured := newQueue(t)
	if err := queue.Send(context.Background(), runs.Message{Topic: "nothing", ID: "01K0000000000000000000000A"}); err == nil {
		t.Error("Send() to a topic no one deployed = nil, want it refused")
	}
	if len(captured.published) != 0 {
		t.Errorf("Send() to a topic no one deployed published %d messages", len(captured.published))
	}
}
