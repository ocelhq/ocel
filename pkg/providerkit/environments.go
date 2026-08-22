package providerkit

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const ProductionEnv = "prod"

type Environment struct {
	Identity  string
	Persisted bool
}

func stackNames(ctx context.Context, records RecordStore, slug string) ([]naming.StackName, error) {
	held, err := records.List(ctx, StacksRecord(slug))
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
	stacks, err := stackNames(ctx, records, slug)
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
	environments := make([]Environment, 0, len(identities))
	for _, identity := range identities {
		environments = append(environments, Environment{Identity: identity, Persisted: persisted[identity]})
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
