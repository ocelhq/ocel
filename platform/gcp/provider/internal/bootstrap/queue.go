package bootstrap

import (
	"context"
	"fmt"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ReasonQueuePurged = "its waiting messages are deleted, since Cloud Tasks reserves a deleted queue's name for 7 days"

var queueKind = KindOps{
	noun:      "Cloud Tasks queue",
	terraform: gcloud.CloudTasksQueueType,
	properties: func(region string, _ Resource) map[string]any {
		return map[string]any{"location": region}
	},
	survey: func(ctx context.Context, b Engine, read Survey, _ Resource) (Presence, error) {
		queue, err := b.readDelayQueue(ctx, read.Tier)
		return Presence{Present: queue != nil}, err
	},
	Make: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.EnsureDelayQueue(ctx, read.Tier)
	},
	removal: func(_ Survey, target Resource) Removal {
		return Removal{Resource: target, Action: provider.ActionUpdate, Reason: ReasonQueuePurged}
	},
	Take: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.purgeDelayQueue(ctx, read.Tier)
	},
	taken: func(r Removal) string { return "Purged " + r.Resource.phrase() + ": " + r.Reason },
}

func (b Engine) DelayQueuePath(tier environment.Tier) string {
	return b.Clients.DelayQueuePath(b.Clients.Region, tier)
}

func (b Engine) readDelayQueue(ctx context.Context, tier environment.Tier) (*cloudtaskspb.Queue, error) {
	client, err := b.Clients.Workload().CloudTasks()
	if err != nil {
		return nil, err
	}
	queue, err := googleretry.Called(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: b.DelayQueuePath(tier)})
	})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s delay queue: %w", b.Clients.DelayQueue(tier), err)
	}
	return queue, nil
}

func (b Engine) EnsureDelayQueue(ctx context.Context, tier environment.Tier) error {
	if err := (topics.Delays{Queue: b.DelayQueuePath(tier)}).Ensure(ctx, b.Clients.Workload()); err != nil {
		return err
	}
	return nil
}

func (b Engine) purgeDelayQueue(ctx context.Context, tier environment.Tier) error {
	client, err := b.Clients.Workload().CloudTasks()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: b.DelayQueuePath(tier)})
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("purge the %s delay queue: %w", b.Clients.DelayQueue(tier), err)
	}
	return nil
}
