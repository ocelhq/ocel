package pgmq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	maxQueueNameLength = 47
	applyLock          = "ocel.pgmq.apply"
)

type Deployment struct {
	Slug    string
	Topics  map[string]*contractv1.ManifestTopic
	Workers map[string]Worker
}

type Worker struct {
	URL         string
	Concurrency int
	Header      http.Header
}

func DeploymentOf(manifest *contractv1.Manifest, workerURLs map[string]string) Deployment {
	deployment := Deployment{Slug: manifest.GetSlug(), Topics: map[string]*contractv1.ManifestTopic{}, Workers: map[string]Worker{}}
	for _, resource := range manifest.GetResources() {
		if topic := resource.GetTopic(); topic != nil {
			deployment.Topics[resource.GetLogicalName()] = topic
		}
	}
	for _, worker := range manifest.GetWorkers() {
		if url, served := workerURLs[worker.GetName()]; served {
			deployment.Workers[worker.GetName()] = Worker{URL: url, Concurrency: int(worker.GetConcurrency())}
		}
	}
	return deployment
}

type deployedConsumer struct {
	topicName string
	topic     *contractv1.ManifestTopic
	consumer  *contractv1.ManifestConsumer
	queue     string
}

func (d Deployment) consumers() map[string]deployedConsumer {
	deployedConsumers := map[string]deployedConsumer{}
	for name, topic := range d.Topics {
		for _, consumer := range topic.GetConsumers() {
			queue := queueName(name, consumer.GetName())
			deployedConsumers[queue] = deployedConsumer{topicName: name, topic: topic, consumer: consumer, queue: queue}
		}
	}
	return deployedConsumers
}

func queueName(topic, consumer string) string {
	name := strings.ReplaceAll(topic+"__"+consumer, "-", "_")
	if len(name) <= maxQueueNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(topic + "/" + consumer))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return name[:maxQueueNameLength-len(suffix)] + suffix
}

func (e *Engine) Apply(ctx context.Context, deployment Deployment) error {
	var canceled []string
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", applyLock); err != nil {
			return fmt.Errorf("wait for other engines applying a deployment: %w", err)
		}
		if err := claimDatabase(ctx, tx, deployment.Slug); err != nil {
			return err
		}
		deployed := deployment.consumers()
		var err error
		if canceled, err = dropUndeployedQueues(ctx, tx, deployed); err != nil {
			return err
		}
		if err := createQueues(ctx, tx, deployed); err != nil {
			return err
		}
		return applySchedules(ctx, tx, deployment)
	})
	if err != nil {
		return err
	}
	for _, execution := range canceled {
		e.stopInFlight(execution)
	}
	e.mu.Lock()
	e.deployment = deployment
	for name, worker := range deployment.Workers {
		if existing, found := e.workerSlots[name]; found {
			existing.setLimit(worker.Concurrency)
		} else {
			e.workerSlots[name] = newSlots(worker.Concurrency)
		}
	}
	e.mu.Unlock()
	e.signalApplied()
	return nil
}

func dropUndeployedQueues(ctx context.Context, tx pgx.Tx, deployed map[string]deployedConsumer) ([]string, error) {
	rows, err := tx.Query(ctx, "SELECT queue_name FROM pgmq.list_queues()")
	if err != nil {
		return nil, fmt.Errorf("list the engine's queues: %w", err)
	}
	queues, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list the engine's queues: %w", err)
	}
	var canceled []string
	for _, queue := range queues {
		if _, still := deployed[queue]; still {
			continue
		}
		executions, err := moveRuns(ctx, tx, runMove{
			from:  unfinishedStatuses,
			to:    provider.RunCanceled,
			where: "queue = @queue",
			set:   "error = @reason, finished_at = clock_timestamp()",
			args:  pgx.NamedArgs{"queue": queue, "reason": "its consumer is no longer deployed"},
		})
		if err != nil {
			return nil, fmt.Errorf("cancel the runs of queue %s: %w", queue, err)
		}
		canceled = append(canceled, executions...)
		if _, err := tx.Exec(ctx, "SELECT pgmq.drop_queue($1)", queue); err != nil {
			return nil, fmt.Errorf("drop queue %s: %w", queue, err)
		}
	}
	return canceled, nil
}

func createQueues(ctx context.Context, tx pgx.Tx, deployed map[string]deployedConsumer) error {
	for queue, consumer := range deployed {
		if _, err := tx.Exec(ctx, "SELECT pgmq.create($1)", queue); err != nil {
			return fmt.Errorf("create queue %s: %w", queue, err)
		}
		if !consumer.topic.GetOrdered() {
			if err := indexLanes(ctx, tx, queue); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, "SELECT pgmq.create_fifo_index($1)", queue); err != nil {
			return fmt.Errorf("index queue %s by key: %w", queue, err)
		}
	}
	return nil
}

func indexLanes(ctx context.Context, tx pgx.Tx, queue string) error {
	for _, lane := range allLanes {
		condition, err := laneCondition(lane)
		if err != nil {
			return err
		}
		var statement string
		if err := tx.QueryRow(ctx, `SELECT format('CREATE INDEX IF NOT EXISTS %I ON pgmq.%I (msg_id) WHERE (message @> %L::jsonb)::integer = 1', q || '_' || $2 || '_idx', q, $3::text)
			FROM pgmq.format_table_name($1, 'q') AS q`, queue, string(runs.LaneOf(lane)), condition).Scan(&statement); err != nil {
			return fmt.Errorf("name the %s lane index of queue %s: %w", runs.LaneOf(lane), queue, err)
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("index queue %s by its %s lane: %w", queue, runs.LaneOf(lane), err)
		}
	}
	return nil
}

func (e *Engine) current() Deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deployment
}

func (e *Engine) workerSlotsOf(worker string) *slots {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.workerSlots[worker]
}

func claimDatabase(ctx context.Context, tx pgx.Tx, slug string) error {
	if slug == "" {
		return errors.New("the deployment names no app slug, and the engine's database serves one app")
	}
	var owner string
	err := tx.QueryRow(ctx, `
		WITH claimed AS (
			INSERT INTO ocel.deployment (slug) VALUES ($1) ON CONFLICT DO NOTHING RETURNING slug
		)
		SELECT slug FROM claimed UNION ALL SELECT slug FROM ocel.deployment LIMIT 1`, slug).Scan(&owner)
	if err != nil {
		return fmt.Errorf("record the app the engine's database serves: %w", err)
	}
	if owner != slug {
		return fmt.Errorf("the engine's database serves app %s, and app %s cannot share it: give %s its own database", owner, slug, slug)
	}
	return nil
}
