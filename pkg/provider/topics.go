package provider

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const (
	MaxTopicTTL           = 14 * 24 * time.Hour
	MaxBatchSize          = 1000
	MaxOrderedBatchSize   = 10
	MaxBatchTimeout       = 300 * time.Second
	MaxOrderedConsumers   = 100
	MaxUnorderedConsumers = 1000
)

type TopicSpec struct {
	Schema    string
	Ordered   bool
	TTL       time.Duration
	Cron      string
	Consumers []ConsumerSpec
}

type ConsumerSpec struct {
	Name        string
	Worker      string
	Exclusive   bool
	Retry       RetryPolicy
	Concurrency int
	MaxDuration time.Duration
	Lanes       []Lane
	Batch       *BatchPolicy
}

type Lane string

const (
	LaneHigh    Lane = "high"
	LaneDefault Lane = "default"
	LaneLow     Lane = "low"
)

type BatchPolicy struct {
	Size    int
	Timeout time.Duration
}

func RefuseTTL(ttl *durationpb.Duration) error {
	if ttl != nil && (ttl.AsDuration() <= 0 || ttl.AsDuration() > MaxTopicTTL) {
		return fmt.Errorf("has ttl %v, and a ttl is more than no time and at most %v (14 days)", ttl.AsDuration(), MaxTopicTTL)
	}
	return nil
}

func RefuseMaxDuration(maxDuration *durationpb.Duration) error {
	if maxDuration != nil && maxDuration.AsDuration() <= 0 {
		return fmt.Errorf("has maxDuration %v, and a run needs more than no time", maxDuration.AsDuration())
	}
	return nil
}

func RefuseBatch(batch *resourcesv1.BatchPolicy) error {
	if batch == nil {
		return nil
	}
	if size := batch.GetSize(); size < 1 || size > MaxBatchSize {
		return fmt.Errorf("has batch size %d, and a batch holds 1 to %d messages", size, MaxBatchSize)
	}
	if timeout := batch.GetTimeout(); timeout != nil && (timeout.AsDuration() < 0 || timeout.AsDuration() > MaxBatchTimeout) {
		return fmt.Errorf("has batch timeout %v, and a batch waits at most %v", timeout.AsDuration(), MaxBatchTimeout)
	}
	return nil
}

func RefuseOrderedConsumer(lanes []topicv1.Lane, batch *resourcesv1.BatchPolicy) error {
	if size := batch.GetSize(); size > MaxOrderedBatchSize {
		return fmt.Errorf("has batch size %d, and a consumer of an ordered topic batches at most %d", size, MaxOrderedBatchSize)
	}
	if len(lanes) > 0 {
		return fmt.Errorf("reads lanes, and a consumer of an ordered topic reads each key in send order whatever its lane: drop lanes or ordered")
	}
	return nil
}

func RefuseOrderedTask(ordered bool, batch *resourcesv1.BatchPolicy) error {
	if ordered && batch != nil {
		return fmt.Errorf("is ordered and batches, and an ordered task runs one run per key at a time: drop ordered or batch")
	}
	return nil
}

func RefuseConsumerCount(count int, ordered bool) error {
	limit, when := MaxUnorderedConsumers, ""
	if ordered {
		limit, when = MaxOrderedConsumers, " when ordered"
	}
	if count > limit {
		return fmt.Errorf("has %d consumers, and a topic takes at most %d consumers%s", count, limit, when)
	}
	return nil
}
