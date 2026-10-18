package gcp

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const reasonQueuePurged = "its waiting messages are deleted and the queue is kept, since Cloud Tasks will not create a queue under a deleted queue's name for 7 days"

var queueKind = kind{
	noun:      "Cloud Tasks queue",
	terraform: tfCloudTasksQueue,
	properties: func(region string, _ item) map[string]any {
		return map[string]any{"location": region}
	},
	survey: func(ctx context.Context, b bootstrap, tier environment.Tier, _ item) (presence, error) {
		queue, err := b.readDelayQueue(ctx, tier)
		return presence{present: queue != nil}, err
	},
	make: func(ctx context.Context, b bootstrap, read survey, _ item) error {
		return b.ensureDelayQueue(ctx, read.Tier)
	},
	removal: func(_ survey, target item) removal {
		return removal{item: target, action: provider.ActionUpdate, reason: reasonQueuePurged}
	},
	take: func(ctx context.Context, b bootstrap, read survey, _ item) (time.Duration, error) {
		return 0, b.purgeDelayQueue(ctx, read.Tier)
	},
	taken: func(r removal) string { return "Purged " + r.item.phrase() + ": " + r.reason },
}

func (b bootstrap) delayQueuePath(tier environment.Tier) string {
	return b.clients.DelayQueuePath(b.clients.Region, tier)
}

func (b bootstrap) readDelayQueue(ctx context.Context, tier environment.Tier) (*cloudtaskspb.Queue, error) {
	client, err := b.clients.Workload().CloudTasks()
	if err != nil {
		return nil, err
	}
	queue, err := googleretry.Called(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: b.delayQueuePath(tier)})
	})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s delay queue: %w", b.clients.DelayQueue(tier), err)
	}
	return queue, nil
}

func (b bootstrap) ensureDelayQueue(ctx context.Context, tier environment.Tier) error {
	if err := (topics.Delays{Queue: b.delayQueuePath(tier)}).Ensure(ctx, b.clients.Workload()); err != nil {
		return err
	}
	return nil
}

func (b bootstrap) purgeDelayQueue(ctx context.Context, tier environment.Tier) error {
	client, err := b.clients.Workload().CloudTasks()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: b.delayQueuePath(tier)})
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("purge the %s delay queue: %w", b.clients.DelayQueue(tier), err)
	}
	return nil
}
