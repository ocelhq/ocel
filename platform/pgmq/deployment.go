package pgmq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const maxQueueName = 47

type Deployment struct {
	Topics  map[string]*contractv1.ManifestTopic
	Workers map[string]Worker
}

type Worker struct {
	URL         string
	Concurrency int
}

func DeploymentOf(manifest *contractv1.Manifest, workerURLs map[string]string) Deployment {
	deployment := Deployment{Topics: map[string]*contractv1.ManifestTopic{}, Workers: map[string]Worker{}}
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

func isTask(topic *contractv1.ManifestTopic) bool {
	return len(topic.GetConsumers()) == 1 && topic.GetConsumers()[0].GetExclusive()
}

func queueName(topic, consumer string) string {
	name := strings.ReplaceAll(topic+"__"+consumer, "-", "_")
	if len(name) <= maxQueueName {
		return name
	}
	sum := sha256.Sum256([]byte(topic + "/" + consumer))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return name[:maxQueueName-len(suffix)] + suffix
}

func (e *Engine) Apply(ctx context.Context, deployment Deployment) error {
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
	e.mu.Unlock()
	e.signalApplied()
	return nil
}

func (e *Engine) current() Deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deployment
}
