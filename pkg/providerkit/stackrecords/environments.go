package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func RecordEnvironmentMeta(ctx context.Context, store records.Store, class edge.Class, slug, env, label string) error {
	name := EnvironmentRecord(class, slug, env)
	held, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	var meta EnvironmentMeta
	if len(held.Bytes) > 0 {
		if err := json.Unmarshal(held.Bytes, &meta); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
	}
	if meta.CreatedAt == 0 {
		meta.CreatedAt = time.Now().Unix()
	}
	if label != "" {
		meta.Label = label
	}
	if held.Bytes, err = json.Marshal(meta); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func EnvironmentMetas(ctx context.Context, records records.Store, class edge.Class, slug string) (map[string]EnvironmentMeta, error) {
	held, err := records.List(ctx, EnvironmentsRecord(class, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's environments: %w", slug, err)
	}
	meta := make(map[string]EnvironmentMeta, len(held))
	for _, record := range held {
		var recorded EnvironmentMeta
		if err := json.Unmarshal(record.Bytes, &recorded); err != nil {
			continue
		}
		meta[record.Name[len(record.Name)-1]] = recorded
	}
	return meta, nil
}

func StackNames(ctx context.Context, records records.Store, class edge.Class, slug string) ([]naming.StackName, error) {
	held, err := records.List(ctx, StacksRecord(class, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's environments: %w", slug, err)
	}
	names := make([]naming.StackName, 0, len(held))
	for _, record := range held {
		stack, err := naming.ParseStackName(record.Name[len(record.Name)-1])
		if err != nil {
			continue
		}
		names = append(names, stack)
	}
	return names, nil
}

func PreviewEnvironments(ctx context.Context, records records.Store, slug string) ([]Environment, error) {
	stacks, err := StackNames(ctx, records, edge.ClassPreview, slug)
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
	meta, err := EnvironmentMetas(ctx, records, edge.ClassPreview, slug)
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
