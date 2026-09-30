package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
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
	Aliases   []edge.PreviewHost
}

type EnvironmentMeta struct {
	Label      string             `json:"label,omitempty"`
	CreatedAt  int64              `json:"created_at,omitempty"`
	Lifecycle  Lifecycle          `json:"lifecycle,omitempty"`
	AliasToken string             `json:"alias_token,omitempty"`
	Aliases    []edge.PreviewHost `json:"aliases,omitempty"`
	Superseded []edge.PreviewHost `json:"superseded,omitempty"`
}

func (m EnvironmentMeta) ListPublishedAliases() []edge.PreviewHost {
	return append(slices.Clone(m.Aliases), m.Superseded...)
}

func (m EnvironmentMeta) RefuseOtherLifecycle(env string, lifecycle Lifecycle) error {
	if m.Lifecycle == "" || m.Lifecycle == lifecycle {
		return nil
	}
	redeploy := "deploy it without --persistent"
	if m.Lifecycle == LifecyclePersistent {
		redeploy = "deploy it with --persistent"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"preview %s was created %s, and this deploy is %s. A preview keeps the lifecycle it was created with: "+
			"%s, remove it with `ocel preview rm %s` first, or deploy this one under another name",
		env, m.Lifecycle, lifecycle, redeploy, env)
}

func ReadEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env string) (EnvironmentMeta, error) {
	name := EnvironmentKey(tier, slug, env)
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return EnvironmentMeta{}, fmt.Errorf("read %s: %w", name, err)
	}
	return decodeEnvironmentMeta(name, recorded)
}

func decodeEnvironmentMeta(name keyvalue.Key, recorded keyvalue.Entry) (EnvironmentMeta, error) {
	var meta EnvironmentMeta
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &meta); err != nil {
			return EnvironmentMeta{}, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return meta, nil
}

func EnsureLifecycle(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env string, lifecycle Lifecycle, aliasToken string) error {
	_, err := changeEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env), false, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if err := meta.RefuseOtherLifecycle(env, lifecycle); err != nil {
			return meta, false, err
		}
		if err := meta.refuseOtherAliasToken(env, aliasToken); err != nil {
			return meta, false, err
		}
		changed := meta.Lifecycle != lifecycle || meta.AliasToken != aliasToken
		meta.Lifecycle, meta.AliasToken = lifecycle, aliasToken
		return meta, changed, nil
	})
	return err
}

func EnsureAliasToken(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) (string, error) {
	meta, err := changeEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env), false, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if meta.AliasToken != "" {
			return meta, false, nil
		}
		meta.AliasToken = token
		return meta, true, nil
	})
	return meta.AliasToken, err
}

func RecordBuiltAliasToken(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) error {
	_, err := changeEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env), false, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if err := meta.refuseOtherAliasToken(env, token); err != nil {
			return meta, false, err
		}
		changed := meta.AliasToken != token
		meta.AliasToken = token
		return meta, changed, nil
	})
	return err
}

func (m EnvironmentMeta) refuseOtherAliasToken(env, token string) error {
	if m.AliasToken == "" || m.AliasToken == token {
		return nil
	}
	return refusal.Refuse(refusal.CodeBusy,
		"preview %s was given another alias while this deploy built for its old one, so ocel did not serve the build on hostnames it was not built for: deploy it again to build for the alias it has now",
		env)
}

func ForgetUnclaimedAlias(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) error {
	name := EnvironmentKey(tier, slug, env)
	return keyvalue.ForgetMatching(ctx, store, name, func(recorded keyvalue.Entry) (bool, error) {
		meta, err := decodeEnvironmentMeta(name, recorded)
		return err == nil && meta.Lifecycle == "" && meta.AliasToken == token, err
	})
}

func changeClaimedEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, aliasToken string, change func(EnvironmentMeta) (EnvironmentMeta, bool, error)) (EnvironmentMeta, error) {
	return changeEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env), true, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if meta.AliasToken != aliasToken {
			return meta, false, refusal.Refuse(refusal.CodeBusy,
				"preview %s was removed and given another alias while this deploy ran, so ocel did not record this deploy over it: deploy it again to serve this build",
				env)
		}
		return change(meta)
	})
}

func changeEnvironmentMeta(ctx context.Context, store keyvalue.Store, name keyvalue.Key, claimed bool, change func(EnvironmentMeta) (EnvironmentMeta, bool, error)) (EnvironmentMeta, error) {
	var next EnvironmentMeta
	existed := claimed
	err := keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		if existed && recorded.Revision == "" {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"preview %s was removed while this deploy ran, so ocel did not record it again: deploy it again to recreate it",
				name.Path[0])
		}
		existed = recorded.Revision != ""
		meta, err := decodeEnvironmentMeta(name, recorded)
		if err != nil {
			return nil, false, err
		}
		var changed bool
		if next, changed, err = change(meta); err != nil || !changed {
			return nil, false, err
		}
		value, err := json.Marshal(next)
		if err != nil {
			return nil, false, fmt.Errorf("record %s: %w", name, err)
		}
		return value, true, nil
	})
	return next, err
}

func RecordEnvironmentMeta(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, aliasToken, label string, lifecycle Lifecycle) error {
	_, err := changeClaimedEnvironmentMeta(ctx, store, tier, slug, env, aliasToken, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if err := meta.RefuseOtherLifecycle(env, lifecycle); err != nil {
			return meta, false, err
		}
		meta.Lifecycle = lifecycle
		if meta.CreatedAt == 0 {
			meta.CreatedAt = time.Now().Unix()
		}
		if label != "" {
			meta.Label = label
		}
		return meta, true, nil
	})
	return err
}

func RecordAliases(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, aliasToken string, aliases []edge.PreviewHost) (previous, superseded []edge.PreviewHost, err error) {
	meta, err := changeClaimedEnvironmentMeta(ctx, store, tier, slug, env, aliasToken, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		previous = meta.Aliases
		return replaceAliases(meta, aliases)
	})
	return previous, meta.Superseded, err
}

func RestoreAliases(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, aliasToken string, recorded, previous []edge.PreviewHost) error {
	_, err := changeClaimedEnvironmentMeta(ctx, store, tier, slug, env, aliasToken, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		if slices.Equal(meta.Aliases, recorded) {
			return replaceAliases(meta, previous)
		}
		published := meta.ListPublishedAliases()
		changed := false
		for _, host := range recorded {
			if !slices.Contains(published, host) {
				meta.Superseded = append(meta.Superseded, host)
				changed = true
			}
		}
		return meta, changed, nil
	})
	return err
}

func replaceAliases(meta EnvironmentMeta, aliases []edge.PreviewHost) (EnvironmentMeta, bool, error) {
	if slices.Equal(meta.Aliases, aliases) {
		return meta, false, nil
	}
	superseded := slices.DeleteFunc(meta.ListPublishedAliases(), func(host edge.PreviewHost) bool { return slices.Contains(aliases, host) })
	meta.Aliases, meta.Superseded = aliases, superseded
	return meta, true, nil
}

func ForgetSuperseded(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env string, withdrawn []edge.PreviewHost) error {
	if len(withdrawn) == 0 {
		return nil
	}
	_, err := changeEnvironmentMeta(ctx, store, EnvironmentKey(tier, slug, env), false, func(meta EnvironmentMeta) (EnvironmentMeta, bool, error) {
		kept := slices.DeleteFunc(slices.Clone(meta.Superseded), func(host edge.PreviewHost) bool { return slices.Contains(withdrawn, host) })
		changed := len(kept) != len(meta.Superseded)
		meta.Superseded = kept
		return meta, changed, nil
	})
	return err
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
			Aliases:   meta[identity].Aliases,
		})
	}
	return environments, nil
}
