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

	pubsub "google.golang.org/api/pubsub/v1"

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

const revoked = "serviceAccount:app@acme-prod.iam.gserviceaccount.com"
const bystander = "serviceAccount:other@acme-prod.iam.gserviceaccount.com"

type policyServer struct {
	mu       sync.Mutex
	policies map[string]*pubsub.Policy
	gone     map[string]bool
	conflict int
	changed  int
	writes   map[string]int
}

func (p *policyServer) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	topic := strings.TrimSuffix(strings.TrimSuffix(path, ":getIamPolicy"), ":setIamPolicy")
	if p.gone[topic] {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND"}}`))
		return
	}
	if strings.HasSuffix(path, ":getIamPolicy") {
		policy := p.policies[topic]
		if policy == nil {
			policy = &pubsub.Policy{Etag: "BwXhoLA="}
		}
		_ = json.NewEncoder(w).Encode(policy)
		return
	}
	if p.conflict > 0 {
		p.conflict--
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":409,"status":"ABORTED"}}`))
		return
	}
	if p.changed > 0 {
		p.changed--
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"error":{"code":412,"status":"FAILED_PRECONDITION"}}`))
		return
	}
	var asked pubsub.SetIamPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.policies[topic] = asked.Policy
	p.writes[topic]++
	_ = json.NewEncoder(w).Encode(asked.Policy)
}

func revoking(t *testing.T, policies map[string]*pubsub.Policy) (*policyServer, topics.Names, *ports.Clients) {
	t.Helper()
	state := &policyServer{policies: map[string]*pubsub.Policy{}, gone: map[string]bool{}, writes: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	t.Cleanup(server.Close)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	for name, policy := range policies {
		state.policies["/v1/"+topics.TopicPath("acme-prod", names.Topic(name))] = policy
	}
	return state, names, &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL}
}

func (p *policyServer) path(names topics.Names, topic string) string {
	return "/v1/" + topics.TopicPath("acme-prod", names.Topic(topic))
}

func membersOf(policy *pubsub.Policy, role string) []string {
	for _, binding := range policy.Bindings {
		if binding.Role == role {
			return binding.Members
		}
	}
	return nil
}

func TestRevokingAPublisherTakesOnlyThatMemberOffEachTopic(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{
			{Role: "roles/pubsub.publisher", Members: []string{revoked, bystander}},
			{Role: "roles/pubsub.viewer", Members: []string{revoked}},
		}},
		"resize": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{bystander}}}},
	})

	taken, err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).RevokePublisher(context.Background(), clients)
	if err != nil {
		t.Fatalf("RevokePublisher() = %v", err)
	}

	orders := state.policies[state.path(names, "orders")]
	if got := membersOf(orders, "roles/pubsub.publisher"); !slices.Equal(got, []string{bystander}) {
		t.Errorf("orders publishers = %q, want only the other member", got)
	}
	if got := membersOf(orders, "roles/pubsub.viewer"); !slices.Equal(got, []string{revoked}) {
		t.Errorf("orders viewers = %q, want the viewer role untouched", got)
	}
	if n := state.writes[state.path(names, "resize")]; n != 0 {
		t.Errorf("resize was written %d times, want none: the member did not hold it", n)
	}
	keys := make([]string, 0, len(taken.Topics))
	for name := range taken.Topics {
		keys = append(keys, name)
	}
	if !slices.Equal(keys, []string{"orders"}) {
		t.Errorf("RevokePublisher() returned topics %q, want only the one the member was taken off", keys)
	}
	if taken.Publisher != revoked || taken.Names != names {
		t.Errorf("RevokePublisher() returned %+v, want the same names and publisher", taken)
	}
}

func TestRevokingTheLastPublisherOfATopicDropsItsBinding(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{revoked}}}},
	})

	if _, err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).RevokePublisher(context.Background(), clients); err != nil {
		t.Fatalf("RevokePublisher() = %v", err)
	}

	if bindings := state.policies[state.path(names, "orders")].Bindings; len(bindings) != 0 {
		t.Errorf("orders binds %+v, want no binding left with no members", bindings)
	}
}

func TestRevokingAPublisherFromATopicThatIsGoneSucceeds(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{revoked}}}},
	})
	state.gone[state.path(names, "resize")] = true

	taken, err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).RevokePublisher(context.Background(), clients)
	if err != nil {
		t.Fatalf("RevokePublisher() = %v, want a deleted topic to have nothing to revoke", err)
	}
	if _, listed := taken.Topics["resize"]; listed {
		t.Errorf("RevokePublisher() returned the deleted topic resize in %v", taken.Topics)
	}
}

func TestRevokingAPublisherRetriesAPolicyChangedUnderIt(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{revoked, bystander}}}},
	})
	state.conflict = 1

	if _, err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).RevokePublisher(context.Background(), clients); err != nil {
		t.Fatalf("RevokePublisher() = %v", err)
	}

	if got := membersOf(state.policies[state.path(names, "orders")], "roles/pubsub.publisher"); !slices.Equal(got, []string{bystander}) {
		t.Errorf("orders publishers = %q after a conflict, want only the other member", got)
	}
}

func TestRevokingAPublisherRetriesAPolicyAnsweredFailedPrecondition(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{revoked, bystander}}}},
	})
	state.changed = 1

	if _, err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).RevokePublisher(context.Background(), clients); err != nil {
		t.Fatalf("RevokePublisher() = %v", err)
	}

	if got := membersOf(state.policies[state.path(names, "orders")], "roles/pubsub.publisher"); !slices.Equal(got, []string{bystander}) {
		t.Errorf("orders publishers = %q after a 412, want only the other member", got)
	}
}

func TestGrantingAPublisherRetriesAPolicyAnsweredFailedPrecondition(t *testing.T) {
	state, names, clients := revoking(t, map[string]*pubsub.Policy{
		"orders": {Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{bystander}}}},
	})
	state.changed = 1

	if err := (topics.Topology{Names: names, Topics: ordersAndResize(), Publisher: revoked}).GrantPublisher(context.Background(), clients); err != nil {
		t.Fatalf("GrantPublisher() = %v", err)
	}

	if got := membersOf(state.policies[state.path(names, "orders")], "roles/pubsub.publisher"); !slices.Contains(got, revoked) {
		t.Errorf("orders publishers = %q after a 412, want the granted member", got)
	}
}
