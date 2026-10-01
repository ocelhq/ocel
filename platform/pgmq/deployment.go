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

type consumerRef struct {
	topicName string
	topic     *contractv1.ManifestTopic
	consumer  *contractv1.ManifestConsumer
	queue     string
}

func (d Deployment) consumers() map[string]consumerRef {
	refs := map[string]consumerRef{}
	for name, topic := range d.Topics {
		for _, consumer := range topic.GetConsumers() {
			queue := queueName(name, consumer.GetName())
			refs[queue] = consumerRef{topicName: name, topic: topic, consumer: consumer, queue: queue}
		}
	}
	return refs
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
	for queue := range deployment.consumers() {
		if _, err := e.pool.Exec(ctx, "SELECT pgmq.create($1)", queue); err != nil {
			return fmt.Errorf("create queue %s: %w", queue, err)
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
