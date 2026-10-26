package topics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func patchedPubSub(t *testing.T) (*googletest.PubSub, *ports.Clients) {
	t.Helper()
	pubsub := googletest.NewPubSub()
	server := httptest.NewServer(pubsub.Serve(t))
	t.Cleanup(server.Close)
	return pubsub, &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL, Records: fake.NewKeyValues()}
}

func shipOrders() map[string]*provider.TopicSpec {
	return map[string]*provider.TopicSpec{"orders": {Consumers: []provider.ConsumerSpec{{Name: "ship", Worker: "worker", Retry: defaultRetry}}}}
}

func TestASubscriptionAWorkerMakesTakesWhatItsTopicsTransformsAsked(t *testing.T) {
	ctx := context.Background()
	pubsub, clients := patchedPubSub(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	asked := map[string]topics.Asked{"orders": {Resource: "topic orders", Result: transform.Result{
		Patches: transform.Patches{"subscription": {"retainAckedMessages": true, "messageRetentionDuration": "86400s"}},
		Tags:    map[string]string{"team": "orders"},
	}}}

	if err := (topics.Topology{Names: names, Topics: shipOrders(), Tags: untagged, Asked: asked}).Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the topology = %v", err)
	}
	if err := (topics.Subscriptions{Names: names, Topics: shipOrders(), Pushes: pushesTo("https://worker.run.app"), Tags: untagged}).Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the subscriptions = %v", err)
	}

	made := pubsub.Subscriptions["projects/acme-prod/subscriptions/"+topicOf(t, names, "orders")+"-ship"]
	if made == nil || !made.RetainAckedMessages || made.MessageRetentionDuration != "86400s" || made.Labels["team"] != "orders" {
		t.Fatalf("made the subscription %+v, want acknowledged messages kept a day and the rule's tag as a label", made)
	}
	if made.PushConfig == nil || !strings.HasPrefix(made.PushConfig.PushEndpoint, "https://worker.run.app/worker") {
		t.Errorf("made the subscription pushing to %+v, want it still pushing to its worker", made.PushConfig)
	}
}

type countedRecords struct {
	records.Store
	mu      sync.Mutex
	changes []string
}

func (c *countedRecords) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	c.note("write " + entry.Key.String())
	return c.Store.Write(ctx, entry)
}

func (c *countedRecords) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	c.note("remove " + key.String())
	return c.Store.Remove(ctx, key, expected)
}

func (c *countedRecords) note(change string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes = append(c.changes, change)
}

func TestARedeployWithNoTransformsReadsNoPubSubResourceAndChangesNoRecord(t *testing.T) {
	ctx := context.Background()
	pubsub := googletest.NewPubSub()
	var mu sync.Mutex
	var reads []string
	serve := pubsub.Serve(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			reads = append(reads, r.URL.Path)
			mu.Unlock()
		}
		serve(w, r)
	}))
	t.Cleanup(server.Close)
	stored := &countedRecords{Store: fake.NewKeyValues()}
	clients := &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: server.URL, Records: stored}
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology := topics.Topology{Names: names, Topics: shipOrders(), Tags: untagged, Asked: map[string]topics.Asked{"orders": {Resource: "topic orders"}}}
	subscriptions := topics.Subscriptions{Names: names, Topics: shipOrders(), Pushes: pushesTo("https://worker.run.app"), Tags: untagged}
	if err := topology.Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the topology = %v", err)
	}
	if err := subscriptions.Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the subscriptions = %v", err)
	}
	reads, stored.changes = nil, nil

	if _, err := topology.ReadDifferences(ctx, clients, shipOrders()); err != nil {
		t.Fatalf("ReadDifferences() = %v", err)
	}
	if err := topology.Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the topology = %v", err)
	}
	if len(reads) > 0 {
		t.Errorf("planning and ensuring the topology read %v from Pub/Sub, want nothing read where no transform patched anything", reads)
	}
	if err := subscriptions.Ensure(ctx, clients); err != nil {
		t.Fatalf("Ensure() of the subscriptions = %v", err)
	}

	if len(stored.changes) > 0 {
		t.Errorf("the redeploy made record changes %v, want none where no transform patched anything", stored.changes)
	}
}
