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

func invalidationTargetsKey(tier environment.Tier, kind edge.Kind, slug string) keyvalue.Key {
	partition := keyvalue.Partition{Tier: tier, Root: keyvalue.RootRouters, Path: []string{string(kind), naming.Sanitize(slug)}}
	return partition.Key("invalidation")
}

func decodeInvalidationTargets(entry keyvalue.Entry) ([]string, error) {
	if len(entry.Value) == 0 {
		return nil, nil
	}
	var targets []string
	if err := json.Unmarshal(entry.Value, &targets); err != nil {
		return nil, fmt.Errorf("read the invalidation targets at %s: %w", entry.Key, err)
	}
	return targets, nil
}

func withoutInvalidationTarget(targets []string, url string) []string {
	return slices.DeleteFunc(slices.Clone(targets), func(target string) bool { return target == url })
}

func recordInvalidationTarget(ctx context.Context, store keyvalue.Store, tier environment.Tier, kind edge.Kind, slug, previous, current string) error {
	if previous != "" && previous != current {
		if err := forgetInvalidationTarget(ctx, store, tier, kind, slug, previous); err != nil {
			return err
		}
	}
	if current == "" {
		return nil
	}
	return keyvalue.Change(ctx, store, invalidationTargetsKey(tier, kind, slug), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		targets, err := decodeInvalidationTargets(recorded)
		if err != nil || slices.Contains(targets, current) {
			return nil, false, err
		}
		targets = append(targets, current)
		slices.Sort(targets)
		value, err := json.Marshal(targets)
		return value, true, err
	})
}

func forgetInvalidationTarget(ctx context.Context, store keyvalue.Store, tier environment.Tier, kind edge.Kind, slug, url string) error {
	if url == "" {
		return nil
	}
	key := invalidationTargetsKey(tier, kind, slug)
	err := keyvalue.Change(ctx, store, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		targets, err := decodeInvalidationTargets(recorded)
		if err != nil {
			return nil, false, err
		}
		remaining := withoutInvalidationTarget(targets, url)
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
		targets, err := decodeInvalidationTargets(recorded)
		return len(withoutInvalidationTarget(targets, url)) == 0, err
	})
}
