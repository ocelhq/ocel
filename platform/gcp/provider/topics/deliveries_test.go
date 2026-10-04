package topics_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func deployedTopics() map[string]*provider.TopicSpec {
	twice := provider.RetryPolicy{MaxAttempts: 2, MinDelay: provider.DefaultRetryMinDelay, MaxDelay: provider.DefaultRetryMaxDelay}
	return map[string]*provider.TopicSpec{
		"resize": {
			Schema:    "{}",
			Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true, Retry: defaultRetry}},
		},
		"slow": {Consumers: []provider.ConsumerSpec{{Name: "slow", Worker: "worker", Exclusive: true, Retry: defaultRetry, MaxDuration: 200 * time.Millisecond}}},
		"orders": {
			TTL: 24 * time.Hour,
			Consumers: []provider.ConsumerSpec{
				{Name: "ship", Worker: "worker", Retry: twice, MaxDuration: 200 * time.Millisecond},
				{Name: "digest", Worker: "worker", Retry: twice, Batch: &provider.BatchPolicy{Size: 10}},
			},
		},
	}
}

type push struct {
	messageID   string
	publishedAt time.Time
	payload     string
	attributes  map[string]string
	orderingKey string
}

func pushBody(p push) []byte {
	attributes := map[string]string{}
	if p.messageID != "" {
		attributes[topics.MessageAttribute] = p.messageID
		attributes[topics.PublishedAtAttribute] = p.publishedAt.UTC().Format(time.RFC3339Nano)
	}
	for name, value := range p.attributes {
		attributes[name] = value
	}
	message := map[string]any{
		"data":        base64.StdEncoding.EncodeToString([]byte(p.payload)),
		"attributes":  attributes,
		"messageId":   "17",
		"orderingKey": p.orderingKey,
	}
	if !p.publishedAt.IsZero() {
		message["publishTime"] = p.publishedAt.UTC().Format(time.RFC3339Nano)
	}
	body, _ := json.Marshal(map[string]any{"message": message, "subscription": "projects/floci-local/subscriptions/whatever"})
	return body
}

func aMessage(payload string) push {
	at := time.Now().UTC().Truncate(time.Millisecond)
	return push{messageID: envelope.NewMessageID(at), publishedAt: at, payload: payload}
}

func TestAPushForAConsumerNoOneDeclaredIsRefused(t *testing.T) {
	deliveries := topics.Deliveries{Topics: deployedTopics(), Worker: "http://127.0.0.1:9"}
	for _, path := range []string{topics.PushPath("orders", "nobody"), topics.PushPath("nothing", "ship"), "/elsewhere"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(pushBody(aMessage(`{}`))))
		recorder := httptest.NewRecorder()
		deliveries.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("a push to %s answered %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestAPushThatIsNotAPubSubMessageIsRefused(t *testing.T) {
	deliveries := topics.Deliveries{Topics: deployedTopics(), Worker: "http://127.0.0.1:9"}
	req := httptest.NewRequest(http.MethodPost, topics.PushPath("orders", "ship"), strings.NewReader("{"))
	recorder := httptest.NewRecorder()
	deliveries.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("a push that is not JSON answered %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

var defaultRetry = provider.RetryPolicy{MaxAttempts: provider.DefaultRetryMaxAttempts, MinDelay: provider.DefaultRetryMinDelay, MaxDelay: provider.DefaultRetryMaxDelay}
