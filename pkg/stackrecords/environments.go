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
	"github.com/ocelhq/ocel/pkg/refusal"
)

const ProductionEnv = "prod"

type Lifecycle string

const (
	LifecycleEphemeral  Lifecycle = "ephemeral"
	LifecyclePersistent Lifecycle = "persistent"
)

type Environment struct {
	Identity  string
	Lifecycle Lifecycle
	Label     string
	CreatedAt int64
}

type EnvironmentMeta struct {
	Label     string    `json:"label,omitempty"`
	CreatedAt int64     `json:"created_at,omitempty"`
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
}

func (m EnvironmentMeta) RefuseOtherLifecycle(env string, lifecycle Lifecycle) error {
	if m.Lifecycle == "" || m.Lifecycle == lifecycle {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"preview %s was created %s, and this deploy is %s. A preview keeps the lifecycle it was created with: "+
			"remove it with `ocel preview rm` first, or deploy this one under another name",
		env, m.Lifecycle, lifecycle)
}

func ReadEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env string) (EnvironmentMeta, error) {
	_, meta, err := readEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env))
	return meta, err
}

func readEnvironmentMeta(ctx context.Context, store keyvalue.Store, name keyvalue.Key) (keyvalue.Entry, EnvironmentMeta, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return keyvalue.Entry{}, EnvironmentMeta{}, fmt.Errorf("read %s: %w", name, err)
	}
	var meta EnvironmentMeta
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &meta); err != nil {
			return keyvalue.Entry{}, EnvironmentMeta{}, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return recorded, meta, nil
}

func RecordEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, label string, lifecycle Lifecycle) error {
	name := EnvironmentKey(tier, slug, env)
	recorded, meta, err := readEnvironmentMeta(ctx, store, name)
	if err != nil {
		return err
	}
	if err := meta.RefuseOtherLifecycle(env, lifecycle); err != nil {
		return err
	}
	meta.Lifecycle = lifecycle
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
	var identities []string
	for _, stack := range stacks {
		if stack.Env == "" || stack.Env == ProductionEnv {
			continue
		}
		if !slices.Contains(identities, stack.Env) {
			identities = append(identities, stack.Env)
		}
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
			Lifecycle: meta[identity].Lifecycle,
			Label:     meta[identity].Label,
			CreatedAt: meta[identity].CreatedAt,
		})
	}
	return environments, nil
}
