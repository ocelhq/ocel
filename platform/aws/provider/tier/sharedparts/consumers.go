package sharedparts

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const consumersKey = "consumers"

func ConsumersPrefix(tier environment.Tier, slug string) records.Key {
	return stackrecords.StacksPartition(tier, slug).Key(consumersKey)
}

func ListUsers(ctx context.Context, store records.Store, tier environment.Tier, slug, subject string) ([]string, error) {
	consumers := ConsumersPrefix(tier, slug)
	entries, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return nil, fmt.Errorf("read who uses the shared %s for the %s tier: %w", subject, tier, err)
	}
	users := []string{}
	for _, entry := range entries {
		if rest, ok := entry.Key.Under(consumers.Path...); ok {
			users = append(users, rest[0])
		}
	}
	slices.Sort(users)
	return slices.Compact(users), nil
}
