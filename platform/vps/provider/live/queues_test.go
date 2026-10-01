package live_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func written(t *testing.T, store keyvalue.Store, key keyvalue.Key, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: key, Value: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestTheQueuesOfATierAreReadBackOnePerEnvironmentWithEverythingRecordedUnderIt(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	tier := environment.TierProduction
	database := live.QueueDatabase{Container: "shop-prod-infra-ocel-queue", Stack: "prod--infra", Sealed: "c2VhbGVk"}
	written(t, store, live.QueueDatabaseKey(tier, "shop", "prod"), database)
	written(t, store, live.QueueTopicKey(tier, "shop", "prod", "send-email"), map[string]any{"ordered": true})
	written(t, store, live.QueueTopicKey(tier, "shop", "prod", "orders"), map[string]any{})
	worker := live.QueueWorker{App: "web", Stack: "prod--web--r1", Container: "shop-prod-web-w-worker", Concurrency: 2}
	written(t, store, live.QueueWorkerKey(tier, "shop", "prod", "worker"), worker)
	written(t, store, live.QueueDatabaseKey(tier, "blog", "prod"), live.QueueDatabase{Container: "blog-db", Stack: "prod--infra"})

	queues, err := live.ReadQueues(context.Background(), store, tier)
	if err != nil {
		t.Fatalf("ReadQueues() = %v", err)
	}
	if len(queues) != 2 {
		t.Fatalf("ReadQueues() = %d queues, want shop's and blog's", len(queues))
	}
	var shop live.Queue
	for _, queue := range queues {
		if queue.Project == "shop" {
			shop = queue
		}
	}
	if shop.Tier != tier || shop.Env != "prod" || shop.Database != database {
		t.Errorf("shop's queue = %s/%s on %+v, want production/prod on %+v", shop.Tier, shop.Env, shop.Database, database)
	}
	if len(shop.Topics) != 2 || string(shop.Topics["send-email"]) != `{"ordered":true}` {
		t.Errorf("shop's topics = %s, want send-email and orders as recorded", shop.Topics)
	}
	if shop.Workers["worker"] != worker {
		t.Errorf("shop's workers = %+v, want the worker container recorded", shop.Workers)
	}
}

func TestAQueueWithNoDatabaseRecordedYetIsNotReadAsOne(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	tier := environment.TierPreview
	written(t, store, live.QueueWorkerKey(tier, "shop", "pr-4", "worker"), live.QueueWorker{App: "web", Container: "w"})

	queues, err := live.ReadQueues(context.Background(), store, tier)
	if err != nil {
		t.Fatalf("ReadQueues() = %v", err)
	}
	if len(queues) != 0 {
		t.Errorf("ReadQueues() = %+v, want nothing: workers with no queue database have nothing to serve", queues)
	}
}
