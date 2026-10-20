package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
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

type valueSecrets struct {
	Secrets  []string               `json:"secrets,omitempty"`
	Releases map[string]releasePins `json:"releases,omitempty"`
}

type releasePins struct {
	Versions map[string][]string `json:"versions,omitempty"`
	Pending  []string            `json:"pending,omitempty"`
}

func (v *valueSecrets) normalize() {
	v.Secrets = sortedUnique(v.Secrets)
	for release, pins := range v.Releases {
		pins.Pending = sortedUnique(pins.Pending)
		for secret, versions := range pins.Versions {
			if pins.Versions[secret] = sortedUnique(versions); len(pins.Versions[secret]) == 0 {
				delete(pins.Versions, secret)
			}
		}
		if len(pins.Versions) == 0 && len(pins.Pending) == 0 {
			delete(v.Releases, release)
			continue
		}
		v.Releases[release] = pins
	}
}

func sortedUnique(values []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(values)))
}

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

func decodeValueSecrets(account string, value []byte) (valueSecrets, error) {
	var recorded valueSecrets
	if len(value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(value, &recorded); err != nil {
		return valueSecrets{}, fmt.Errorf("read the secrets that hold the values of %s: %w", account, err)
	}
	return recorded, nil
}

func readValueSecrets(ctx context.Context, store records.Store, tier environment.Tier, account string) (valueSecrets, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, valueSecretsKey(tier, account))
	if err != nil {
		return valueSecrets{}, err
	}
	return decodeValueSecrets(account, recorded.Value)
}

func ReadValueSecrets(ctx context.Context, store records.Store, tier environment.Tier, account string) ([]string, error) {
	recorded, err := readValueSecrets(ctx, store, tier, account)
	return recorded.Secrets, err
}

func changeValueSecrets(ctx context.Context, store records.Store, tier environment.Tier, account string, change func(*valueSecrets)) error {
	key := valueSecretsKey(tier, account)
	return records.RetryWhileStale(ctx, func() (bool, error) {
		recorded, err := records.ReadOrEmpty(ctx, store, key)
		if err != nil {
			return false, err
		}
		held, err := decodeValueSecrets(account, recorded.Value)
		if err != nil {
			return false, err
		}
		if held.Releases == nil {
			held.Releases = map[string]releasePins{}
		}
		change(&held)
		held.normalize()
		encoded, err := json.Marshal(held)
		if err != nil {
			return false, err
		}
		empty := len(held.Secrets) == 0 && len(held.Releases) == 0
		switch {
		case bytes.Equal(encoded, recorded.Value), empty && len(recorded.Value) == 0:
			return false, nil
		case empty:
			err = store.Remove(ctx, key, recorded.Revision)
		default:
			recorded.Value = encoded
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
		return nil, nil
	}
	keys := slices.DeleteFunc(slices.Sorted(maps.Keys(values)), func(key string) bool { return values[key] == "" })
	if len(keys) == 0 {
		return nil, nil
	}
	account, release := c.AppAccount(ref.Project, ref.Name.Env, app), ref.Name.String()
	secrets := make(map[string]string, len(keys))
	for _, key := range keys {
		secret, err := c.AppValueSecret(ref.Project, ref.Name.Env, app, key)
		if err != nil {
			return nil, err
		}
		secrets[key] = secret
	}
	if err := changeValueSecrets(ctx, p.Records, ref.Tier, account, func(held *valueSecrets) {
		pins := held.Releases[release]
		held.Secrets = append(held.Secrets, slices.Collect(maps.Values(secrets))...)
		pins.Pending = append(pins.Pending, slices.Collect(maps.Values(secrets))...)
		held.Releases[release] = pins
	}); err != nil {
		return nil, err
	}
	service, err := c.Secrets()
	if err != nil {
		return nil, err
	}
	pinned := make(run.PinnedValues, len(keys))
	for _, key := range keys {
		version, err := c.EnsureLatestVersion(ctx, service, ref.Tier, secrets[key], []byte(values[key]))
		if err != nil {
			return nil, err
		}
		pinned[key] = run.SecretVersion{Secret: secrets[key], Version: version}
	}
	if err := changeValueSecrets(ctx, p.Records, ref.Tier, account, func(held *valueSecrets) {
		pins := held.Releases[release]
		if pins.Versions == nil {
			pins.Versions = map[string][]string{}
		}
		for _, version := range pinned {
			pins.Pending = slices.DeleteFunc(pins.Pending, func(secret string) bool { return secret == version.Secret })
			pins.Versions[version.Secret] = append(pins.Versions[version.Secret], version.Version)
		}
		held.Releases[release] = pins
	}); err != nil {
		return nil, err
	}
	return pinned, nil
}

func (p *Release) valueGrants(ctx context.Context, tier environment.Tier, account, member string) ([]grants.Grant, error) {
	recorded, err := readValueSecrets(ctx, p.Records, tier, account)
	if err != nil {
		return nil, err
	}
	var pinned []string
	for _, pins := range recorded.Releases {
		pinned = append(pinned, slices.Collect(maps.Keys(pins.Versions))...)
	}
	granted := []grants.Grant{}
	for _, secret := range sortedUnique(pinned) {
		granted = append(granted, grants.On(grants.Secret, secret, ValueReaderRole, member))
	}
	return granted, nil
}

func (p *Release) destroyUnpinnedVersions(ctx context.Context, ref provider.StackRef) error {
	if ref.Name.IsInfra() {
		return nil
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	account := c.AppAccount(ref.Project, ref.Name.Env, ref.Name.App)
	listed, err := readValueSecrets(ctx, p.Records, ref.Tier, account)
	if err != nil || len(listed.Secrets) == 0 {
		return err
	}
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	enabled := make(map[string][]string, len(listed.Secrets))
	for _, secret := range listed.Secrets {
		if enabled[secret], err = c.ListEnabledVersions(ctx, service, secret); err != nil {
			return err
		}
	}
	recorded, err := readValueSecrets(ctx, p.Records, ref.Tier, account)
	if err != nil {
		return err
	}
	stacks, err := stackrecords.List(ctx, p.Records, ref.Tier, ref.Project)
	if err != nil {
		return err
	}
	kept, pending, gone := map[string][]string{}, map[string]bool{}, map[string]releasePins{}
	for release, pins := range recorded.Releases {
		if !slices.ContainsFunc(stacks, func(stack stackrecords.NamedStack) bool { return stack.Name.String() == release }) {
			gone[release] = pins
			continue
		}
		for _, secret := range pins.Pending {
			pending[secret] = true
		}
		for secret, versions := range pins.Versions {
			kept[secret] = append(kept[secret], versions...)
		}
	}
	for secret, versions := range enabled {
		if pending[secret] {
			continue
		}
		for _, version := range versions[min(1, len(versions)):] {
			if slices.Contains(kept[secret], version) {
				continue
			}
			if err := c.DestroyVersion(ctx, service, secret, version); err != nil {
				return err
			}
		}
	}
	if len(gone) == 0 {
		return nil
	}
	return changeValueSecrets(ctx, p.Records, ref.Tier, account, func(held *valueSecrets) {
		for release, pins := range gone {
			if samePins(held.Releases[release], pins) {
				delete(held.Releases, release)
			}
		}
	})
}

func samePins(a, b releasePins) bool {
	return slices.Equal(a.Pending, b.Pending) && maps.EqualFunc(a.Versions, b.Versions, slices.Equal)
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
