package manifest

import (
	"regexp"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	maxAttempts           = 100
	maxRetryDelay         = 600 * time.Second
	maxConcurrency        = 1000
	maxTTL                = 14 * 24 * time.Hour
	maxBatchSize          = 1000
	maxOrderedBatchSize   = 10
	maxBatchTimeout       = 300 * time.Second
	maxOrderedConsumers   = 100
	maxUnorderedConsumers = 1000
	maxNameBytes          = 63
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func refuseInvalidName(d declaredResource, name string) error {
	if len(name) <= maxNameBytes && namePattern.MatchString(name) {
		return nil
	}
	return refuse(d, "is named %q, and a name is lowercase letters and digits in words joined by single hyphens, at most %d characters", name, maxNameBytes)
}

func refuseTopicLimits(d declaredResource) error {
	return refuseRetry(d, d.Topic.GetRetry())
}

func refuseWorkerLimits(d declaredResource) error {
	return refuseConcurrency(d, d.Worker.GetConcurrency())
}

func refuseConsumerLimits(d declaredResource) error {
	return refuseRunLimits(d, topicConsumer(d))
}

func refuseTaskLimits(d declaredResource) error {
	task := d.Task
	if err := firstRefusal(refuseRetry(d, task.GetRetry()), refuseRunLimits(d, taskConsumer(d))); err != nil {
		return err
	}
	if ttl := task.GetTtl(); ttl != nil && (ttl.AsDuration() <= 0 || ttl.AsDuration() > maxTTL) {
		return refuse(d, "has ttl %v, and a ttl is more than no time and at most %v (14 days)", ttl.AsDuration(), maxTTL)
	}
	if task.GetOrdered() && task.GetBatch() != nil {
		return refuse(d, "is ordered and batches, and an ordered task runs one run per key at a time: drop ordered or batch")
	}
	if cron := task.GetCron(); cron != "" {
		if err := refuseInvalidCron(cron); err != nil {
			return refuse(d, "has an invalid schedule: %v", err)
		}
	}
	return nil
}

func refuseRunLimits(d declaredResource, runs *contractv1.ManifestConsumer) error {
	return firstRefusal(
		refuseRetry(d, runs.GetRetry()),
		refuseConcurrency(d, runs.GetConcurrency()),
		refuseMaxDuration(d, runs.GetMaxDuration()),
		refuseBatch(d, runs.GetBatch()),
	)
}

func refuseRetry(d declaredResource, retry *resourcesv1.RetryPolicy) error {
	if retry == nil {
		return nil
	}
	if attempts := retry.GetMaxAttempts(); attempts != 0 && (attempts < 1 || attempts > maxAttempts) {
		return refuse(d, "retries with maxAttempts %d, and maxAttempts is 1 to %d", attempts, maxAttempts)
	}
	minDelay, maxDelay := retry.GetMinDelay(), retry.GetMaxDelay()
	if minDelay != nil && minDelay.AsDuration() < 0 {
		return refuse(d, "retries with minDelay %v, and a delay is never negative", minDelay.AsDuration())
	}
	if maxDelay != nil && (maxDelay.AsDuration() < 0 || maxDelay.AsDuration() > maxRetryDelay) {
		return refuse(d, "retries with maxDelay %v, and maxDelay is at most %v", maxDelay.AsDuration(), maxRetryDelay)
	}
	if minDelay != nil && maxDelay != nil && minDelay.AsDuration() > maxDelay.AsDuration() {
		return refuse(d, "retries with minDelay %v above its maxDelay %v", minDelay.AsDuration(), maxDelay.AsDuration())
	}
	return nil
}

func refuseConcurrency(d declaredResource, concurrency int32) error {
	if concurrency != 0 && (concurrency < 1 || concurrency > maxConcurrency) {
		return refuse(d, "has concurrency %d, and concurrency is 1 to %d", concurrency, maxConcurrency)
	}
	return nil
}

func refuseMaxDuration(d declaredResource, maxDuration *durationpb.Duration) error {
	if maxDuration != nil && maxDuration.AsDuration() <= 0 {
		return refuse(d, "has maxDuration %v, and a run needs more than no time", maxDuration.AsDuration())
	}
	return nil
}

func refuseBatch(d declaredResource, batch *resourcesv1.BatchPolicy) error {
	if batch == nil {
		return nil
	}
	if size := batch.GetSize(); size < 1 || size > maxBatchSize {
		return refuse(d, "has batch size %d, and a batch holds 1 to %d messages", size, maxBatchSize)
	}
	if timeout := batch.GetTimeout(); timeout != nil && (timeout.AsDuration() < 0 || timeout.AsDuration() > maxBatchTimeout) {
		return refuse(d, "has batch timeout %v, and a batch waits at most %v", timeout.AsDuration(), maxBatchTimeout)
	}
	return nil
}
