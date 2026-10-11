package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
)

func purgeTargetsKey(tier environment.Tier, kind edge.Kind, slug string) keyvalue.Key {
	partition := keyvalue.Partition{Tier: tier, Root: keyvalue.RootRouters, Path: []string{string(kind), naming.Sanitize(slug)}}
	return partition.Key("invalidation")
}

func decodePurgeTargets(entry keyvalue.Entry) ([]string, error) {
	if len(entry.Value) == 0 {
		return nil, nil
	}
	var targets []string
	if err := json.Unmarshal(entry.Value, &targets); err != nil {
		return nil, fmt.Errorf("read the purge targets at %s: %w", entry.Key, err)
	}
	return targets, nil
}

func withoutPurgeTarget(targets []string, url string) []string {
	return slices.DeleteFunc(slices.Clone(targets), func(target string) bool { return target == url })
}

func recordPurgeTarget(ctx context.Context, store keyvalue.Store, tier environment.Tier, kind edge.Kind, slug, previous, current string) error {
	if previous != "" && previous != current {
		if err := forgetPurgeTarget(ctx, store, tier, kind, slug, previous); err != nil {
			return err
		}
	}
	if current == "" {
		return nil
	}
	return keyvalue.Change(ctx, store, purgeTargetsKey(tier, kind, slug), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		targets, err := decodePurgeTargets(recorded)
		if err != nil || slices.Contains(targets, current) {
			return nil, false, err
		}
		targets = append(targets, current)
		slices.Sort(targets)
		value, err := json.Marshal(targets)
		return value, true, err
	})
}

func forgetPurgeTarget(ctx context.Context, store keyvalue.Store, tier environment.Tier, kind edge.Kind, slug, url string) error {
	if url == "" {
		return nil
	}
	key := purgeTargetsKey(tier, kind, slug)
	err := keyvalue.Change(ctx, store, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		targets, err := decodePurgeTargets(recorded)
		if err != nil {
			return nil, false, err
		}
		remaining := withoutPurgeTarget(targets, url)
		if len(remaining) == 0 || len(remaining) == len(targets) {
			return nil, false, nil
		}
		value, err := json.Marshal(remaining)
		return value, true, err
	})
	if err != nil {
		return err
	}
	return keyvalue.ForgetMatching(ctx, store, key, func(recorded keyvalue.Entry) (bool, error) {
		targets, err := decodePurgeTargets(recorded)
		return len(withoutPurgeTarget(targets, url)) == 0, err
	})
}
