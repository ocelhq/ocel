package topics_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const invoker = "ocel-production@floci-local.iam.gserviceaccount.com"

func ordersAndResize() map[string]*provider.TopicSpec {
	return map[string]*provider.TopicSpec{
		"orders": {
			Ordered: true,
			Consumers: []provider.ConsumerSpec{
				{Name: "ship", Worker: "worker", Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: 2 * time.Second, MaxDelay: 90 * time.Second}},
				{Name: "bill", Worker: "billing", Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: 5 * time.Second, MaxDelay: 90 * time.Second}},
			},
		},
		"resize": {Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true, Retry: defaultRetry}}},
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

	if err := (topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app")}).Ensure(context.Background(), clients); err != nil {
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

func TestGrantingAPublisherTouchesOnlyTheTopicsNotTheirDeadLetters(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"etag":"BwXhoLA="}`))
	}))
	t.Cleanup(server.Close)
	clients := &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL}
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}

	err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: "serviceAccount:app@acme-prod.iam.gserviceaccount.com"}).GrantPublisher(context.Background(), clients)
	if err != nil {
		t.Fatalf("GrantPublisher() = %v", err)
	}

	var written []string
	for _, call := range calls {
		if strings.HasPrefix(call, "POST ") && strings.HasSuffix(call, ":setIamPolicy") {
			written = append(written, call)
		}
	}
	want := []string{
		"POST /v1/projects/acme-prod/topics/" + names.Topic("orders") + ":setIamPolicy",
		"POST /v1/projects/acme-prod/topics/" + names.Topic("resize") + ":setIamPolicy",
	}
	slices.Sort(written)
	if !slices.Equal(written, want) {
		t.Errorf("GrantPublisher() wrote %q, want the policies of the two topics alone", written)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "PUT ") || strings.Contains(call, "/subscriptions/") || strings.Contains(call, "dead") {
			t.Errorf("GrantPublisher() called %q, and granting a publisher creates and reads nothing else", call)
		}
	}
}
