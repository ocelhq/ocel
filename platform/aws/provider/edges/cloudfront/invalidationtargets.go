package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
)

const invalidationAttempts = 8

func invalidationPartition(tier environment.Tier, slug string) keyvalue.Partition {
	partition := keyvalue.Partition{Tier: tier, Root: keyvalue.RootRouters, Path: []string{string(Kind)}}
	if slug != "" {
		partition.Path = append(partition.Path, naming.Sanitize(slug))
	}
	return partition
}

type invalidationTargets struct {
	keyValues keyvalue.Store
	partition keyvalue.Partition
}

func (t invalidationTargets) note(ctx context.Context, distribution string) error {
	return t.set(ctx, distribution, true)
}

func (t invalidationTargets) forget(ctx context.Context, distribution string) error {
	return t.set(ctx, distribution, false)
}

func (t invalidationTargets) set(ctx context.Context, distribution string, noted bool) error {
	if distribution == "" {
		return fmt.Errorf("note an invalidation target for %s: it names no front to invalidate", t.partition)
	}
	at := t.partition.Key("invalidation")
	for range invalidationAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, t.keyValues, at)
		if err != nil {
			return fmt.Errorf("read the invalidation targets for %s: %w", t.partition, err)
		}
		var targets []string
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &targets); err != nil {
				return fmt.Errorf("read the invalidation targets for %s: %w", t.partition, err)
			}
		}
		kept := slices.DeleteFunc(slices.Clone(targets), func(target string) bool { return target == distribution })
		if noted {
			kept = append(kept, distribution)
		}
		slices.Sort(kept)
		if slices.Equal(kept, targets) {
			return nil
		}
		if err := t.write(ctx, recorded, kept); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return fmt.Errorf("record the invalidation targets for %s: %w", t.partition, err)
		}
		return nil
	}
	return fmt.Errorf("record the invalidation targets for %s: they moved under %d attempts", t.partition, invalidationAttempts)
}

func (t invalidationTargets) write(ctx context.Context, recorded keyvalue.Entry, targets []string) error {
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
