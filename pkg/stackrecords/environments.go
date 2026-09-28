package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
)

const ProductionEnv = "prod"

type Environment struct {
	Identity  string
	Persisted bool
	Label     string
	CreatedAt int64
}

type EnvironmentMeta struct {
	Label     string `json:"label,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

func RecordEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, label string) error {
	name := EnvironmentKey(tier, slug, env)
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	var meta EnvironmentMeta
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &meta); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
	}
	if meta.CreatedAt == 0 {
		meta.CreatedAt = time.Now().Unix()
	}
	if label != "" {
		meta.Label = label
	}
	if recorded.Value, err = json.Marshal(meta); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func EnvironmentMetas(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string) (map[string]EnvironmentMeta, error) {
	recorded, err := store.List(ctx, EnvironmentsPartition(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's environments: %w", slug, err)
	}
	meta := make(map[string]EnvironmentMeta, len(recorded))
	for _, entry := range recorded {
		var recorded EnvironmentMeta
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			continue
		}
		meta[entry.Key.Path[0]] = recorded
	}
	return meta, nil
}

func StackNames(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string) ([]naming.StackName, error) {
	recorded, err := store.List(ctx, StacksPartition(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's environments: %w", slug, err)
	}
	names := make([]naming.StackName, 0, len(recorded))
	for _, entry := range recorded {
		stack, err := naming.ParseStackName(entry.Key.Path[0])
		if err != nil {
			continue
		}
		names = append(names, stack)
	}
	return names, nil
}

func PreviewEnvironments(ctx context.Context, store keyvalue.Store, slug string) ([]Environment, error) {
	stacks, err := StackNames(ctx, store, environment.TierPreview, slug)
	if err != nil {
		return nil, err
	}
	persisted := map[string]bool{}
	var identities []string
	for _, stack := range stacks {
		if stack.Env == "" || stack.Env == ProductionEnv {
			continue
		}
		if !slices.Contains(identities, stack.Env) {
			identities = append(identities, stack.Env)
		}
		persisted[stack.Env] = persisted[stack.Env] || stack.IsInfra()
	}
	slices.Sort(identities)
	meta, err := EnvironmentMetas(ctx, store, environment.TierPreview, slug)
	if err != nil {
		return nil, err
	}
	environments := make([]Environment, 0, len(identities))
	for _, identity := range identities {
		environments = append(environments, Environment{
			Identity:  identity,
			Persisted: persisted[identity],
			Label:     meta[identity].Label,
			CreatedAt: meta[identity].CreatedAt,
		})
	}
	return environments, nil
}
