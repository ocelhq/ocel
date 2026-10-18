package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	invalidationAttempts     = 8
	invalidationRetryBase    = 100 * time.Millisecond
	invalidationRetryCeiling = 2 * time.Second
)

func newInvalidationPartition(tier environment.Tier, slug string) records.Partition {
	partition := records.Partition{Tier: tier, Root: stackrecords.RootRouters, Path: []string{string(Kind)}}
	if slug != "" {
		partition.Path = append(partition.Path, naming.Sanitize(slug))
	}
	return partition
}

type invalidationTargets struct {
	keyValues records.Store
	partition records.Partition
	wait      func(context.Context, time.Duration) error
}

func (t invalidationTargets) add(ctx context.Context, distribution string) error {
	return t.rewrite(ctx, distribution, func(targets []string) []string {
		return append(withoutTarget(targets, distribution), distribution)
	})
}

func (t invalidationTargets) remove(ctx context.Context, distribution string) error {
	return t.rewrite(ctx, distribution, func(targets []string) []string {
		return withoutTarget(targets, distribution)
	})
}

func withoutTarget(targets []string, distribution string) []string {
	return slices.DeleteFunc(slices.Clone(targets), func(target string) bool { return target == distribution })
}

func (t invalidationTargets) rewrite(ctx context.Context, distribution string, change func([]string) []string) error {
	if distribution == "" {
		return fmt.Errorf("record an invalidation target for %s: it names no front to invalidate", t.partition)
	}
	at := t.partition.Key("invalidation")
	for attempt := range invalidationAttempts {
		if attempt > 0 {
			if err := t.backoff(ctx, attempt-1); err != nil {
				return err
			}
		}
		recorded, err := records.ReadOrEmpty(ctx, t.keyValues, at)
		if err != nil {
			return fmt.Errorf("read the invalidation targets for %s: %w", t.partition, err)
		}
		var targets []string
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &targets); err != nil {
				return fmt.Errorf("read the invalidation targets for %s: %w", t.partition, err)
			}
		}
		changed := change(targets)
		slices.Sort(changed)
		if slices.Equal(changed, targets) {
			return nil
		}
		if err := t.write(ctx, recorded, changed); err != nil {
			if errors.Is(err, records.ErrStale) {
				continue
			}
			return fmt.Errorf("record the invalidation targets for %s: %w", t.partition, err)
		}
		return nil
	}
	return fmt.Errorf("record the invalidation targets for %s: they moved under %d attempts", t.partition, invalidationAttempts)
}

func (t invalidationTargets) backoff(ctx context.Context, attempt int) error {
	delay := jitteredDelay(invalidationRetryBase, invalidationRetryCeiling, attempt, rand.Float64())
	if t.wait != nil {
		return t.wait(ctx, delay)
	}
	return waitFor(ctx, delay)
}

func (t invalidationTargets) write(ctx context.Context, recorded records.Entry, targets []string) error {
	if len(targets) == 0 {
		return t.keyValues.Remove(ctx, recorded.Key, recorded.Revision)
	}
	encoded, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	recorded.Value = encoded
	_, err = t.keyValues.Write(ctx, recorded)
	return err
}
