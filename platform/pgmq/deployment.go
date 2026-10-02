package pgmq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const maxQueueNameLength = 47

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
	if err := e.claimDatabase(ctx, deployment.Slug); err != nil {
		return err
	}
	for queue, deployed := range deployment.consumers() {
		if _, err := e.pool.Exec(ctx, "SELECT pgmq.create($1)", queue); err != nil {
			return fmt.Errorf("create queue %s: %w", queue, err)
		}
		if !deployed.topic.GetOrdered() {
			continue
		}
		if _, err := e.pool.Exec(ctx, "SELECT pgmq.create_fifo_index($1)", queue); err != nil {
			return fmt.Errorf("index queue %s by key: %w", queue, err)
		}
	}
	if err := e.applySchedules(ctx, deployment); err != nil {
		return err
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

func (e *Engine) claimDatabase(ctx context.Context, slug string) error {
	if slug == "" {
		return errors.New("the deployment names no app slug, and the engine's database serves one app")
	}
	var owner string
	err := e.pool.QueryRow(ctx, `
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
