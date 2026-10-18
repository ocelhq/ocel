package tasks

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	idleAfter = 5 * time.Minute
	idleEvery = time.Minute
)

type Consumption struct {
	engine    *Engine
	receiver  *Receiver
	idleAfter time.Duration
	idleEvery time.Duration
}

func NewConsumption(engine *Engine, client receivingQueues) *Consumption {
	return &Consumption{engine: engine, receiver: NewReceiver(engine.queue, client, engine.FireCron), idleAfter: idleAfter, idleEvery: idleEvery}
}

type servedQueue struct {
	topic    string
	consumer string
	queue    string
}

func (e *Engine) queuesServed() []servedQueue {
	var served []servedQueue
	for name, topic := range e.queue.topology.Topics {
		if topic.Declared == nil {
			continue
		}
		for _, consumer := range topic.Declared.Consumers {
			if consumer.Worker == e.worker {
				served = append(served, servedQueue{topic: name, consumer: consumer.Name, queue: topic.Queues[consumer.Name]})
			}
		}
	}
	slices.SortFunc(served, func(a, b servedQueue) int { return cmp.Compare(a.queue, b.queue) })
	return served
}

func (c *Consumption) Run(ctx context.Context) error {
	served := c.engine.queuesServed()
	if len(served) == 0 {
		return fmt.Errorf("worker %q serves no queue: no consumer in the deployment's topics runs on it", c.engine.worker)
	}
	urls := make([]string, 0, len(served))
	for _, queue := range served {
		url, err := c.engine.queue.url(ctx, queue.queue)
		if err != nil {
			return err
		}
		urls = append(urls, url)
	}
	var running sync.WaitGroup
	for _, queue := range served {
		running.Go(func() { c.engine.runs.Serve(ctx, c.receiver, queue.topic, queue.consumer) })
	}
	if _, scales := c.engine.queue.services.serviceOf(c.engine.worker); scales {
		running.Go(func() { c.scaleInWhenIdle(ctx, urls) })
	}
	running.Wait()
	return nil
}

func (c *Consumption) scaleInWhenIdle(ctx context.Context, urls []string) {
	ticker := time.NewTicker(c.idleEvery)
	defer ticker.Stop()
	scaledAt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		busySince, idle := c.receiver.idleSince()
		if scaledAt.After(busySince) {
			busySince = scaledAt
		}
		if !idle || time.Since(busySince) < c.idleAfter {
			continue
		}
		holding, err := c.holdsMessages(ctx, urls)
		if err != nil {
			slog.Warn("read what a worker's queues hold", "error", err)
			continue
		}
		if holding {
			continue
		}
		if err := c.engine.queue.services.scaleInToFloor(ctx, c.engine.worker); err != nil {
			slog.Warn("scale an idle worker's service in to its floor", "error", err)
			continue
		}
		scaledAt = time.Now()
	}
}

func (c *Consumption) holdsMessages(ctx context.Context, urls []string) (bool, error) {
	for _, url := range urls {
		out, err := c.receiver.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(url),
			AttributeNames: []sqstypes.QueueAttributeName{
				sqstypes.QueueAttributeNameApproximateNumberOfMessages,
				sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		if err != nil {
			return false, err
		}
		for _, count := range out.Attributes {
			if n, err := strconv.Atoi(count); err != nil || n > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}
