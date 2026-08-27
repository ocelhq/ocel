package providerkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const ProductionEnv = "prod"

const PreviewTTL = 7 * 24 * time.Hour

type Environment struct {
	Identity  string
	Persisted bool
	Label     string
	CreatedAt int64
	ExpiresAt int64
}

type EnvironmentMeta struct {
	Label     string `json:"label,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

func recordEnvironmentMeta(ctx context.Context, records RecordStore, class Class, slug, env, label string, ephemeral bool) error {
	name := EnvironmentRecord(class, slug, env)
	held, err := Held(ctx, records, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	var meta EnvironmentMeta
	if len(held.Bytes) > 0 {
		if err := json.Unmarshal(held.Bytes, &meta); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
	}
	now := time.Now().Unix()
	if meta.CreatedAt == 0 {
		meta.CreatedAt = now
	}
	meta.ExpiresAt = 0
	if ephemeral {
		meta.ExpiresAt = now + int64(PreviewTTL.Seconds())
	}
	if label != "" {
		meta.Label = label
	}
	if held.Bytes, err = json.Marshal(meta); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := records.Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func environmentMeta(ctx context.Context, records RecordStore, class Class, slug string) (map[string]EnvironmentMeta, error) {
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

func stackNames(ctx context.Context, records RecordStore, class Class, slug string) ([]naming.StackName, error) {
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

func previewEnvironments(ctx context.Context, records RecordStore, slug string) ([]Environment, error) {
	stacks, err := stackNames(ctx, records, ClassPreview, slug)
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
	meta, err := environmentMeta(ctx, records, ClassPreview, slug)
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
			ExpiresAt: meta[identity].ExpiresAt,
		})
	}
	return environments, nil
}

func envName(env *environmentv1.Environment) (string, error) {
	class, err := classOf(env.GetTier())
	if err != nil {
		return "", err
	}
	if class == ClassProduction {
		return ProductionEnv, nil
	}
	identity := env.GetIdentity()
	if identity == "" {
		return "", Refuse(CodeInvalid, "a preview environment is addressed by its identity, and this call names none")
	}
	if err := naming.Validate("preview name", identity); err != nil {
		return "", Refuse(CodeInvalid, "%s", err.Error())
	}
	if identity == ProductionEnv {
		return "", Refuse(CodeInvalid, "%q names production, so it is not a preview environment's identity", identity)
	}
	return identity, nil
}

func lifecycleOf(persisted bool) environmentv1.Lifecycle {
	if persisted {
		return environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	}
	return environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}
