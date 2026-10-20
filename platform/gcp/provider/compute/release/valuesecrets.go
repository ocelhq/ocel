package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	ValueReaderRole    = "roles/secretmanager.secretAccessor"
	valueSecretsRecord = "appvaluesecrets"
)

func SensitiveValuesOf(app *provider.AppSpec, probeToken string) map[string]string {
	values := maps.Clone(app.Values.Sensitive)
	if probeToken == "" {
		return values
	}
	if values == nil {
		values = map[string]string{}
	}
	values[originguard.ProbeTokenVar] = probeToken
	return values
}

func valueSecretsKey(tier environment.Tier, account string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(valueSecretsRecord, account)
}

func ReadValueSecrets(ctx context.Context, store records.Store, tier environment.Tier, account string) ([]string, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, valueSecretsKey(tier, account))
	if err != nil || len(recorded.Value) == 0 {
		return nil, err
	}
	var secrets []string
	if err := json.Unmarshal(recorded.Value, &secrets); err != nil {
		return nil, fmt.Errorf("read the secrets that hold the values of %s: %w", account, err)
	}
	return secrets, nil
}

func changeValueSecrets(ctx context.Context, store records.Store, tier environment.Tier, account string, change func([]string) []string) error {
	key := valueSecretsKey(tier, account)
	return records.RetryWhileStale(ctx, func() (bool, error) {
		recorded, err := records.ReadOrEmpty(ctx, store, key)
		if err != nil {
			return false, err
		}
		var held []string
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &held); err != nil {
				return false, fmt.Errorf("read the secrets that hold the values of %s: %w", account, err)
			}
		}
		changed := change(slices.Clone(held))
		slices.Sort(changed)
		changed = slices.Compact(changed)
		if slices.Equal(changed, held) {
			return false, nil
		}
		if len(changed) == 0 {
			err = store.Remove(ctx, key, recorded.Revision)
		} else if recorded.Value, err = json.Marshal(changed); err == nil {
			_, err = store.Write(ctx, recorded)
		}
		if errors.Is(err, records.ErrStale) || errors.Is(err, records.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("record the secrets that hold the values of %s: %w", account, err)
		}
		return false, nil
	})
}

func (p *Release) PinValues(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, app string, values map[string]string) (run.PinnedValues, error) {
	if len(values) == 0 || p.SensitiveInEnv {
		// HACK: floci-gcp refuses a Cloud Run env valueSource, so an emulated service takes its sensitive values as literals; drop SensitiveInEnv once floci-gcp resolves secretKeyRef.
		return run.PinnedValues{}, nil
	}
	keys := slices.DeleteFunc(slices.Sorted(maps.Keys(values)), func(key string) bool { return values[key] == "" })
	if len(keys) == 0 {
		return run.PinnedValues{}, nil
	}
	account := c.AppAccount(ref.Project, ref.Name.Env, app)
	secrets := make(map[string]string, len(keys))
	for _, key := range keys {
		secret, err := c.AppValueSecret(ref.Project, ref.Name.Env, app, key)
		if err != nil {
			return run.PinnedValues{}, err
		}
		secrets[key] = secret
	}
	if err := changeValueSecrets(ctx, p.Records, ref.Tier, account, func(held []string) []string {
		return append(held, slices.Collect(maps.Values(secrets))...)
	}); err != nil {
		return run.PinnedValues{}, err
	}
	service, err := c.Secrets()
	if err != nil {
		return run.PinnedValues{}, err
	}
	pinned := run.PinnedValues{Owner: account, Versions: make(map[string]run.SecretVersion, len(keys))}
	for _, key := range keys {
		version, err := c.EnsureLatestVersion(ctx, service, ref.Tier, secrets[key], []byte(values[key]))
		if err != nil {
			return run.PinnedValues{}, err
		}
		pinned.Versions[key] = run.SecretVersion{Secret: secrets[key], Version: version}
	}
	return pinned, nil
}

func (p *Release) valueGrants(ctx context.Context, tier environment.Tier, account, member string) ([]grants.Grant, error) {
	secrets, err := ReadValueSecrets(ctx, p.Records, tier, account)
	if err != nil {
		return nil, err
	}
	granted := make([]grants.Grant, 0, len(secrets))
	for _, secret := range secrets {
		granted = append(granted, grants.On(grants.Secret, secret, ValueReaderRole, member))
	}
	return granted, nil
}

func (p *Release) pruneValues(ctx context.Context, ref provider.StackRef, progress progress.Log) {
	if err := p.destroyUnpinnedValues(ctx, ref); err != nil {
		gcloud.EnsureProgress(progress).Warn(fmt.Sprintf("Left the older versions of app %s's values in environment %s in place, and the next release that retires one tries again: %v", ref.Name.App, ref.Name.Env, err))
	}
}

func (p *Release) destroyUnpinnedValues(ctx context.Context, ref provider.StackRef) error {
	if ref.Name.IsInfra() {
		return nil
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	account := c.AppAccount(ref.Project, ref.Name.Env, ref.Name.App)
	secrets, err := ReadValueSecrets(ctx, p.Records, ref.Tier, account)
	if err != nil || len(secrets) == 0 {
		return err
	}
	pinned, err := p.Services.ListPinnedVersions(ctx, account)
	if err != nil {
		return err
	}
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	var unpinned []string
	for _, secret := range secrets {
		if len(pinned[secret]) > 0 {
			if err := c.DestroyUnpinnedVersions(ctx, service, secret, pinned[secret]); err != nil {
				return err
			}
			continue
		}
		if err := c.DeleteSecret(ctx, service, ref.Tier, secret); err != nil {
			return err
		}
		unpinned = append(unpinned, secret)
	}
	if len(unpinned) == 0 {
		return nil
	}
	return changeValueSecrets(ctx, p.Records, ref.Tier, account, func(held []string) []string {
		return slices.DeleteFunc(held, func(secret string) bool { return slices.Contains(unpinned, secret) })
	})
}

func removeValueSecrets(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, account string) error {
	secrets, err := ReadValueSecrets(ctx, store, tier, account)
	if err != nil {
		return err
	}
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if err := c.DeleteSecret(ctx, service, tier, secret); err != nil {
			return err
		}
	}
	return records.Forget(ctx, store, valueSecretsKey(tier, account))
}
