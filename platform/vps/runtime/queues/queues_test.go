package queues_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/pgmq"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/runtime/queues"
)

const tier = environment.TierProduction

type cipher struct{}

func (cipher) Open(_ context.Context, _ environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	for _, field := range bound {
		if field.Name == "stack" && field.Value != "prod--infra" {
			return nil, errors.New("sealed to another stack")
		}
	}
	return []byte("opened:" + string(sealed)), nil
}

type located map[string]string

func (l located) Address(_ context.Context, container string) (string, error) {
	if address, found := l[container]; found {
		return address, nil
	}
	return "", errors.New("no such container")
}

type engine struct {
	config  pgmq.Config
	mu      sync.Mutex
	applied []pgmq.Deployment
	closed  bool
}

func (e *engine) Apply(_ context.Context, deployment pgmq.Deployment) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applied = append(e.applied, deployment)
	return nil
}

func (e *engine) Dispatch(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (e *engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
}

func (e *engine) Handlers() (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler) {
	return taskv1connect.UnimplementedTaskServiceHandler{}, topicv1connect.UnimplementedTopicServiceHandler{}
}

func (e *engine) deployments() []pgmq.Deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.applied)
}

type engines struct {
	mu     sync.Mutex
	opened []*engine
}

func (o *engines) open(_ context.Context, cfg pgmq.Config) (queues.Engine, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e := &engine{config: cfg}
	o.opened = append(o.opened, e)
	return e, nil
}

func record(t *testing.T, store keyvalue.Store, key keyvalue.Key, value any) {
	t.Helper()
	raw, ok := value.([]byte)
	if !ok {
		var err error
		if raw, err = json.Marshal(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := keyvalue.Change(context.Background(), store, key, func(keyvalue.Entry) ([]byte, bool, error) { return raw, true, nil }); err != nil {
		t.Fatal(err)
	}
}

func aQueue(t *testing.T, store keyvalue.Store) {
	t.Helper()
	record(t, store, live.QueueDatabaseKey(tier, "shop", "prod"), live.QueueDatabase{
		Container: "shop-prod-infra-ocel-queue", Stack: "prod--infra", Sealed: base64.StdEncoding.EncodeToString([]byte("pw")),
	})
	record(t, store, live.QueueTopicKey(tier, "shop", "prod", "send-email"),
		[]byte(`{"consumers":[{"name":"send-email","worker":"worker","exclusive":true}]}`))
	record(t, store, live.QueueWorkerKey(tier, "shop", "prod", "worker"), live.QueueWorker{App: "web", Stack: "prod--web--r1", Container: "shop-worker", Concurrency: 3})
	record(t, store, live.QueueWorkerKey(tier, "shop", "prod", "ledger"), live.QueueWorker{App: "web", Stack: "prod--web--r1", Container: "shop-ledger"})
}

func aHost(store keyvalue.Store, opened *engines) *queues.Engines {
	return &queues.Engines{
		IsAnswering: func(context.Context, string) bool { return true },
		Records:     store,
		Tiers:       []environment.Tier{tier},
		Cipher:      cipher{},
		Addresses:   located{"shop-prod-infra-ocel-queue": "10.0.0.2", "shop-worker": "10.0.0.3"},
		Open:        opened.open,
	}
}

func TestARecordedQueueIsServedByAnEngineOnItsDatabaseDeliveringToTheWorkersThatRun(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	aQueue(t, store)
	opened := &engines{}
	host := aHost(store, opened)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := host.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(opened.opened) != 1 {
		t.Fatalf("%d engines were opened, want one for shop's prod queue", len(opened.opened))
	}
	served := opened.opened[0]
	server, err := url.Parse(served.config.ServerURL)
	if err != nil {
		t.Fatal(err)
	}
	if password, _ := server.User.Password(); server.Host != "10.0.0.2:5432" || server.User.Username() != "postgres" || password != "opened:pw" {
		t.Errorf("the engine opened %s, want the queue database's address with its opened password", served.config.ServerURL)
	}
	if served.config.Database != live.QueueDatabaseName {
		t.Errorf("the engine uses database %q, want %q", served.config.Database, live.QueueDatabaseName)
	}
	deployments := served.deployments()
	if len(deployments) != 1 {
		t.Fatalf("the engine was applied %d times, want once", len(deployments))
	}
	applied := deployments[0]
	if applied.Slug != "shop" || len(applied.Topics) != 1 || applied.Topics["send-email"].GetConsumers()[0].GetWorker() != "worker" {
		t.Errorf("the engine was applied %+v, want shop's send-email task", applied)
	}
	if want := (pgmq.Worker{URL: "http://10.0.0.3:8080", Concurrency: 3}); applied.Workers["worker"] != want {
		t.Errorf("worker is delivered to as %+v, want %+v", applied.Workers["worker"], want)
	}
	if _, found := applied.Workers["ledger"]; found {
		t.Error("ledger, whose container is not running, is delivered to")
	}
	if _, _, found := host.Served(tier, "shop", "prod"); !found {
		t.Error("shop's prod queue is not served to its containers")
	}
	if _, _, found := host.Served(tier, "shop", "pr-4"); found {
		t.Error("a queue nothing recorded is served")
	}
}

func TestAnUnchangedQueueIsAppliedOnceAndAChangedOneAgain(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	aQueue(t, store)
	opened := &engines{}
	host := aHost(store, opened)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	for range 3 {
		if err := host.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if applied := len(opened.opened[0].deployments()); applied != 1 {
		t.Fatalf("an unchanged queue was applied %d times, want once", applied)
	}
	record(t, store, live.QueueTopicKey(tier, "shop", "prod", "resize"), []byte(`{"consumers":[{"name":"resize","worker":"worker","exclusive":true}]}`))
	if err := host.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if applied := opened.opened[0].deployments(); len(applied) != 2 || len(applied[1].Topics) != 2 {
		t.Errorf("after a task was declared the engine was applied %+v, want the new task applied", applied)
	}
	if len(opened.opened) != 1 {
		t.Errorf("a changed topic opened %d engines, want the one already serving", len(opened.opened))
	}
}

func TestAQueueNoLongerRecordedIsClosedAndServedNoMore(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	aQueue(t, store)
	opened := &engines{}
	host := aHost(store, opened)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := host.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := keyvalue.Forget(ctx, store, live.QueueDatabaseKey(tier, "shop", "prod")); err != nil {
		t.Fatal(err)
	}
	if err := host.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !opened.opened[0].closed {
		t.Error("the engine of a removed queue is still open")
	}
	if _, _, found := host.Served(tier, "shop", "prod"); found {
		t.Error("a removed queue is still served")
	}
}

func TestAQueueWhoseDatabaseIsNotRunningIsLeftUnservedAndTheOthersStillServe(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	aQueue(t, store)
	record(t, store, live.QueueDatabaseKey(tier, "blog", "prod"), live.QueueDatabase{Container: "blog-gone", Stack: "prod--infra"})
	opened := &engines{}
	host := aHost(store, opened)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := host.Reconcile(ctx); err == nil {
		t.Error("Reconcile() = nil, want the queue it could not reach reported")
	}
	if _, _, found := host.Served(tier, "shop", "prod"); !found {
		t.Error("shop's queue is unserved because blog's database is down")
	}
	if _, _, found := host.Served(tier, "blog", "prod"); found {
		t.Error("blog's queue is served with no database")
	}
}

func TestAWorkerIsDeliveredToOnlyOnceItsContainerAnswers(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	aQueue(t, store)
	opened := &engines{}
	host := aHost(store, opened)
	var answering sync.Map
	host.IsAnswering = func(_ context.Context, url string) bool {
		_, answers := answering.Load(url)
		return answers
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := host.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if applied := opened.opened[0].deployments(); len(applied) != 1 || len(applied[0].Workers) != 0 {
		t.Fatalf("a worker still starting was applied as %+v, and every delivery to it would be spent as a failed attempt", applied)
	}
	answering.Store("http://10.0.0.3:8080", true)
	if err := host.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	applied := opened.opened[0].deployments()
	if len(applied) != 2 || applied[1].Workers["worker"].URL != "http://10.0.0.3:8080" {
		t.Errorf("once it answers the engine was applied %+v, want worker delivered to", applied)
	}
}
