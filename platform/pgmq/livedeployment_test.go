//go:build integration

package pgmq

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestADatabaseServesOnlyTheDeploymentFirstAppliedToIt(t *testing.T) {
	ctx := context.Background()
	cfg := aDatabase(t)
	shop, blog := openedOn(t, cfg), openedOn(t, cfg)
	orders := map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"})}

	if err := shop.Apply(ctx, Deployment{Slug: "shop", Topics: orders}); err != nil {
		t.Fatalf("Apply(shop): %v", err)
	}
	err := blog.Apply(ctx, Deployment{Slug: "blog", Topics: orders})
	if err == nil || !strings.Contains(err.Error(), "shop") || !strings.Contains(err.Error(), "blog") {
		t.Errorf("Apply(blog) on shop's database = %v, want a refusal naming both", err)
	}
	if err := shop.Apply(ctx, Deployment{Slug: "shop", Topics: orders}); err != nil {
		t.Errorf("Apply(shop) again: %v", err)
	}
}

func TestADeploymentWithoutASlugIsRefused(t *testing.T) {
	if err := anEngine(t).Apply(context.Background(), Deployment{}); err == nil {
		t.Error("Apply took a deployment that names no app")
	}
}

func openedOn(t *testing.T, cfg Config) *Engine {
	t.Helper()
	engine, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func queuesOf(t *testing.T, engine *Engine) []string {
	t.Helper()
	rows, err := engine.pool.Query(context.Background(), "SELECT queue_name FROM pgmq.list_queues() ORDER BY queue_name")
	if err != nil {
		t.Fatalf("list queues: %v", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list queues: %v", err)
	}
	return names
}

func statusOfConsumer(t *testing.T, engine *Engine, consumer string) (provider.RunStatus, bool) {
	t.Helper()
	var status string
	var finished bool
	if err := engine.pool.QueryRow(context.Background(), "SELECT status, finished_at IS NOT NULL FROM ocel.runs WHERE consumer = $1", consumer).Scan(&status, &finished); err != nil {
		t.Fatalf("read %s's run: %v", consumer, err)
	}
	return provider.RunStatus(status), finished
}

func TestAConsumerDroppedFromTheDeploymentTakesItsQueueAndCancelsItsUnfinishedRuns(t *testing.T) {
	ctx := context.Background()
	email, audit := &contractv1.ManifestConsumer{Name: "email", Worker: "mail"}, &contractv1.ManifestConsumer{Name: "audit", Worker: "mail"}
	engine := applied(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(email, audit)}, nil)
	message := runs.NewMessageID(time.Now())
	for consumer, status := range map[string]provider.RunStatus{"audit": provider.RunExecuting, "email": provider.RunQueued} {
		run := runs.Run{Execution: runs.ExecutionOf(message, consumer), Topic: "orders", Consumer: consumer, Status: status, MaxAttempts: 3, Message: message, CreatedAt: time.Now(), DueAt: time.Now()}
		if _, err := engine.Store().CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
	}

	if err := engine.Apply(ctx, Deployment{Slug: "app", Topics: map[string]*contractv1.ManifestTopic{"orders": aTopic(email)}}); err != nil {
		t.Fatalf("Apply without audit: %v", err)
	}

	if got := queuesOf(t, engine); !slices.Equal(got, []string{"orders__email"}) {
		t.Errorf("queues = %v, want only email's: a dropped consumer's queue is no one's to read", got)
	}
	if status, finished := statusOfConsumer(t, engine, "audit"); status != provider.RunCanceled || !finished {
		t.Errorf("audit's executing run is %s (finished %v), want it canceled now nothing will settle it", status, finished)
	}
	if status, _ := statusOfConsumer(t, engine, "email"); status != provider.RunQueued {
		t.Errorf("email's run is %s, want it still queued", status)
	}
}

func TestADeploymentThatFailsToApplyLeavesNoQueueOrClaimBehind(t *testing.T) {
	ctx := context.Background()
	engine := anEngine(t)
	topics := map[string]*contractv1.ManifestTopic{
		"orders":  aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"}),
		"nightly": aTask(named("nightly"), func(topic *contractv1.ManifestTopic) { topic.Cron = "not a cron" }),
	}

	if err := engine.Apply(ctx, Deployment{Slug: "shop", Topics: topics}); err == nil {
		t.Fatal("Apply took a task whose cron does not parse")
	}

	if got := queuesOf(t, engine); len(got) != 0 {
		t.Errorf("queues = %v after a failed Apply, want none", got)
	}
	if err := engine.Apply(ctx, Deployment{Slug: "blog"}); err != nil {
		t.Errorf("Apply(blog) after shop's Apply failed = %v, want the database still unclaimed", err)
	}
}

func TestADeploymentWhoseDroppedRunsFailToCancelStillWakesTheDispatcher(t *testing.T) {
	ctx := context.Background()
	email, audit := &contractv1.ManifestConsumer{Name: "email", Worker: "mail"}, &contractv1.ManifestConsumer{Name: "audit", Worker: "mail"}
	engine := applied(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(email, audit)}, nil)
	<-engine.applied
	message := runs.NewMessageID(time.Now())
	run := runs.Run{Execution: runs.ExecutionOf(message, "audit"), Topic: "orders", Consumer: "audit", Status: provider.RunExecuting, MaxAttempts: 3, Message: message, CreatedAt: time.Now(), DueAt: time.Now()}
	if _, err := engine.Store().CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := engine.pool.Exec(ctx, `CREATE FUNCTION ocel.refuse() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'refused'; END$$`); err != nil {
		t.Fatalf("create the refusing trigger function: %v", err)
	}
	if _, err := engine.pool.Exec(ctx, `CREATE TRIGGER refuse BEFORE UPDATE ON ocel.runs FOR EACH ROW EXECUTE FUNCTION ocel.refuse()`); err != nil {
		t.Fatalf("refuse run updates: %v", err)
	}
	bill := &contractv1.ManifestConsumer{Name: "bill", Worker: "mail"}

	if err := engine.Apply(ctx, Deployment{Slug: "app", Topics: map[string]*contractv1.ManifestTopic{"orders": aTopic(email), "invoices": aTopic(bill)}}); err == nil {
		t.Fatal("Apply hid that audit's runs failed to cancel")
	}

	select {
	case <-engine.applied:
	default:
		t.Error("the dispatcher was never told of the new deployment, so invoices' queue goes unread")
	}
}
