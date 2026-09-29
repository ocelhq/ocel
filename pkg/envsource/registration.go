package envsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const registerAttempts = 5

type Registration struct {
	Project    string     `json:"project"`
	Descriptor Descriptor `json:"descriptor"`
	Folders    []string   `json:"folders"`
	DedupeKey  string     `json:"dedupeKey"`
}

func (r Registration) Credentials() []variablestore.Cell {
	var out []variablestore.Cell
	for _, name := range r.Descriptor.CredentialVariables() {
		out = append(out, variablestore.Cell{Key: name})
	}
	return out
}

func registrationsPartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvSources}
}

func registrationKey(tier environment.Tier, project string) keyvalue.Key {
	return registrationsPartition(tier).Key(project)
}

func Register(ctx context.Context, store variablestore.Store, tier environment.Tier, registration Registration) (Registration, error) {
	key, err := DedupeKey(ctx, store, variablestore.Scope{Project: registration.Project, Tier: tier}, registration.Descriptor)
	if err != nil {
		return Registration{}, err
	}
	registration.DedupeKey = key
	registration.Folders = slices.Compact(slices.Sorted(slices.Values(registration.Folders)))
	encoded, err := json.Marshal(registration)
	if err != nil {
		return Registration{}, err
	}
	name := registrationKey(tier, registration.Project)
	for range registerAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, store.KeyValues, name)
		if err != nil {
			return Registration{}, err
		}
		previous, err := registrationOf(recorded)
		if err != nil {
			return Registration{}, err
		}
		recorded.Value = encoded
		_, err = store.KeyValues.Write(ctx, recorded)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return Registration{}, err
		}
		return registration, moveSharer(ctx, store.KeyValues, tier, registration.Project, previous.DedupeKey, key)
	}
	return Registration{}, fmt.Errorf("%s's env source registration was rewritten under every attempt to record it; another deploy of it is still running", registration.Project)
}

func RestoreRegistration(ctx context.Context, store variablestore.Store, tier environment.Tier, replacing Registration, previous *Registration) error {
	name := registrationKey(tier, replacing.Project)
	recorded, err := store.KeyValues.Read(ctx, name)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	replaced, err := json.Marshal(replacing)
	if err != nil || !bytes.Equal(recorded.Value, replaced) {
		return err
	}
	if previous == nil {
		err := store.KeyValues.Remove(ctx, name, recorded.Revision)
		if errors.Is(err, keyvalue.ErrStale) || errors.Is(err, keyvalue.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return release(ctx, store.KeyValues, tier, replacing.DedupeKey, replacing.Project)
	}
	if recorded.Value, err = json.Marshal(previous); err != nil {
		return err
	}
	_, err = store.KeyValues.Write(ctx, recorded)
	if errors.Is(err, keyvalue.ErrStale) {
		return nil
	}
	if err != nil {
		return err
	}
	return moveSharer(ctx, store.KeyValues, tier, replacing.Project, replacing.DedupeKey, previous.DedupeKey)
}

func rekey(ctx context.Context, store variablestore.Store, tier environment.Tier, project string) error {
	recorded, err := store.KeyValues.Read(ctx, registrationKey(tier, project))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := registrationOf(recorded)
	if err != nil {
		return err
	}
	key, err := DedupeKey(ctx, store, variablestore.Scope{Project: project, Tier: tier}, current.Descriptor)
	if err != nil || key == current.DedupeKey {
		return err
	}
	previous := current.DedupeKey
	current.DedupeKey = key
	if recorded.Value, err = json.Marshal(current); err != nil {
		return err
	}
	_, err = store.KeyValues.Write(ctx, recorded)
	if errors.Is(err, keyvalue.ErrStale) {
		return nil
	}
	if err != nil {
		return err
	}
	return moveSharer(ctx, store.KeyValues, tier, project, previous, key)
}

func registrationOf(recorded keyvalue.Entry) (Registration, error) {
	var out Registration
	if len(recorded.Value) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(recorded.Value, &out); err != nil {
		return Registration{}, fmt.Errorf("read %s: %w", recorded.Key, err)
	}
	return out, nil
}

func Registered(ctx context.Context, store keyvalue.Store, tier environment.Tier, project string) (Registration, bool, error) {
	recorded, err := store.Read(ctx, registrationKey(tier, project))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	out, err := registrationOf(recorded)
	if err != nil {
		return Registration{}, false, err
	}
	return out, true, nil
}

func Registrations(ctx context.Context, store keyvalue.Store, tier environment.Tier) ([]Registration, error) {
	recorded, err := store.List(ctx, registrationsPartition(tier))
	if err != nil {
		return nil, fmt.Errorf("read the %s env source registrations: %w", tier, err)
	}
	out := make([]Registration, 0, len(recorded))
	for _, entry := range recorded {
		var registration Registration
		if err := json.Unmarshal(entry.Value, &registration); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		out = append(out, registration)
	}
	slices.SortFunc(out, func(a, b Registration) int { return strings.Compare(a.Project, b.Project) })
	return out, nil
}
