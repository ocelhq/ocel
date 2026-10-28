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
	"github.com/ocelhq/ocel/pkg/topiclimits"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	maxQueueNameLength = 47
	applyLock          = "ocel.pgmq.apply"
	undeployedReason   = "its consumer is no longer deployed"
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

type queueOf struct {
	topic, consumer string
}

func (d Deployment) queues() map[string]queueOf {
	deployed := map[string]queueOf{}
	for name, topic := range d.Topics {
		for _, consumer := range topic.GetConsumers() {
			deployed[queueName(name, consumer.GetName())] = queueOf{topic: name, consumer: consumer.GetName()}
		}
	}
	return deployed
}

func (d Deployment) consumer(topicName, consumerName string) (*contractv1.ManifestTopic, *contractv1.ManifestConsumer, bool) {
	topic, found := d.Topics[topicName]
	if !found {
		return nil, nil, false
	}
	for _, consumer := range topic.GetConsumers() {
		if consumer.GetName() == consumerName {
			return topic, consumer, true
		}
	}
	return nil, nil, false
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
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", applyLock); err != nil {
			return fmt.Errorf("wait for other engines applying a deployment: %w", err)
		}
		if err := claimDatabase(ctx, tx, deployment.Slug); err != nil {
			return err
		}
		deployed := deployment.queues()
		if err := dropUndeployedQueues(ctx, tx, deployed); err != nil {
			return err
		}
		if err := createQueues(ctx, tx, deployment, deployed); err != nil {
			return err
		}
		return applySchedules(ctx, tx, deployment)
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.deployment = deployment
	e.mu.Unlock()
	e.runs.Deploy(runsDeployment(deployment))
	e.signalApplied()
	return e.cancelUndeployedRuns(ctx, deployment)
}

func (e *Engine) cancelUndeployedRuns(ctx context.Context, deployment Deployment) error {
	rows, err := e.pool.Query(ctx, "SELECT DISTINCT topic, consumer FROM ocel.runs WHERE finished_at IS NULL")
	if err != nil {
		return fmt.Errorf("list the consumers with unfinished runs: %w", err)
	}
	pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queueOf, error) {
		var pair queueOf
		return pair, row.Scan(&pair.topic, &pair.consumer)
	})
	if err != nil {
		return fmt.Errorf("list the consumers with unfinished runs: %w", err)
	}
	var errs []error
	for _, pair := range pairs {
		if _, _, deployed := deployment.consumer(pair.topic, pair.consumer); deployed {
			continue
		}
		if err := e.runs.CancelRunsOf(ctx, pair.topic, pair.consumer, undeployedReason); err != nil {
			errs = append(errs, fmt.Errorf("cancel the runs of %s/%s: %w", pair.topic, pair.consumer, err))
		}
	}
	return errors.Join(errs...)
}

func runsDeployment(deployment Deployment) runs.Deployment {
	converted := runs.Deployment{Topics: map[string]*provider.TopicSpec{}, Workers: map[string]runs.Worker{}}
	for name, topic := range deployment.Topics {
		converted.Topics[name] = topicSpecOf(topic)
	}
	for name, worker := range deployment.Workers {
		converted.Workers[name] = runs.Worker{URL: worker.URL, Client: clientFor(worker), Concurrency: worker.Concurrency}
	}
	return converted
}

func topicSpecOf(topic *contractv1.ManifestTopic) *provider.TopicSpec {
	spec := &provider.TopicSpec{Schema: topic.GetSchema(), Ordered: topic.GetOrdered(), TTL: topic.GetTtl().AsDuration(), Cron: topic.GetCron()}
	for _, consumer := range topic.GetConsumers() {
		declared := provider.ConsumerSpec{
			Name:        consumer.GetName(),
			Worker:      consumer.GetWorker(),
			Exclusive:   consumer.GetExclusive(),
			Retry:       topiclimits.ResolveRetryPolicy(topic.GetRetry(), consumer.GetRetry()),
			Concurrency: int(consumer.GetConcurrency()),
			MaxDuration: consumer.GetMaxDuration().AsDuration(),
		}
		for _, lane := range consumer.GetLanes() {
			declared.Lanes = append(declared.Lanes, runs.LaneOf(lane))
		}
		if batch := consumer.GetBatch(); batch.GetSize() > 0 {
			declared.Batch = &provider.BatchPolicy{Size: int(batch.GetSize()), Timeout: batch.GetTimeout().AsDuration()}
		}
		spec.Consumers = append(spec.Consumers, declared)
	}
	return spec
}

func dropUndeployedQueues(ctx context.Context, tx pgx.Tx, deployed map[string]queueOf) error {
	rows, err := tx.Query(ctx, "SELECT queue_name FROM pgmq.list_queues()")
	if err != nil {
		return fmt.Errorf("list the engine's queues: %w", err)
	}
	queues, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list the engine's queues: %w", err)
	}
	for _, queue := range queues {
		if _, still := deployed[queue]; still {
			continue
		}
		if _, err := tx.Exec(ctx, "SELECT pgmq.drop_queue($1)", queue); err != nil {
			return fmt.Errorf("drop queue %s: %w", queue, err)
		}
	}
	return nil
}

func createQueues(ctx context.Context, tx pgx.Tx, deployment Deployment, deployed map[string]queueOf) error {
	for queue, of := range deployed {
		if _, err := tx.Exec(ctx, "SELECT pgmq.create($1)", queue); err != nil {
			return fmt.Errorf("create queue %s: %w", queue, err)
		}
		if !deployment.Topics[of.topic].GetOrdered() {
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

func clientFor(worker Worker) *http.Client {
	if len(worker.Header) == 0 {
		return tracing.NewClient(0)
	}
	return &http.Client{Transport: headerTransport{header: worker.Header, next: tracing.NewTransport(nil)}}
}

type headerTransport struct {
	header http.Header
	next   http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for name, values := range t.header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return t.next.RoundTrip(req)
}
