package envsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const (
	RootRegistrations records.Root = "envsources"
	RootStatus        records.Root = "envsourcestatus"
	RootDigestKey     records.Root = "envsourcedigestkey"

	removalSegment = "removal"
)

type Registration struct {
	Project    string     `json:"project"`
	Descriptor Descriptor `json:"descriptor"`
	Folders    []string   `json:"folders"`
	DedupeKey  string     `json:"dedupeKey"`
}

type recordedRegistration struct {
	Project    string          `json:"project"`
	Descriptor json.RawMessage `json:"descriptor"`
	Folders    []string        `json:"folders"`
	DedupeKey  string          `json:"dedupeKey"`
}

type UndecodableRegistrationError struct {
	Project string
	Kind    string
	Err     error
}

func (e *UndecodableRegistrationError) Error() string {
	return fmt.Sprintf("the %s env source a deploy registered for %s no longer decodes, so nothing reads it until %s deploys again and registers it anew: %v", e.Kind, e.Project, e.Project, e.Err)
}

func (e *UndecodableRegistrationError) Unwrap() error { return e.Err }

func isUndecodable(err error) bool {
	var undecodable *UndecodableRegistrationError
	return errors.As(err, &undecodable)
}

func (r Registration) Credentials() []variablestore.Cell {
	var out []variablestore.Cell
	for _, name := range r.Descriptor.CredentialVariables() {
		out = append(out, variablestore.Cell{Key: name})
	}
	return out
}

func registrationsPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootRegistrations}
}

func registrationKey(tier environment.Tier, project string) records.Key {
	return registrationsPartition(tier).Key(project)
}

func removalKey(tier environment.Tier, project string) records.Key {
	return registrationsPartition(tier).Key(project, removalSegment)
}

type removal struct {
	RemovedAt time.Time `json:"removedAt"`
}

func recordRemoval(ctx context.Context, store records.Store, tier environment.Tier, project string, at time.Time) error {
	encoded, err := json.Marshal(removal{RemovedAt: at})
	if err != nil {
		return err
	}
	return records.Change(ctx, store, removalKey(tier, project), func(records.Entry) ([]byte, bool, error) {
		return encoded, true, nil
	})
}

func isRemoved(ctx context.Context, store records.Store, tier environment.Tier, project string) (bool, error) {
	_, err := store.Read(ctx, removalKey(tier, project))
	if errors.Is(err, records.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func forgetRemovalsBefore(ctx context.Context, store records.Store, removals []records.Entry, before time.Time) error {
	var failed []error
	for _, recorded := range removals {
		var removed removal
		if err := json.Unmarshal(recorded.Value, &removed); err != nil {
			failed = append(failed, fmt.Errorf("read %s: %w", recorded.Key, err))
			continue
		}
		if !removed.RemovedAt.Before(before) {
			continue
		}
		if err := records.ForgetMatching(ctx, store, recorded.Key, func(current records.Entry) (bool, error) {
			return current.Revision == recorded.Revision, nil
		}); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
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
	var previous Registration
	err = records.Change(ctx, store.KeyValues, registrationKey(tier, registration.Project), func(recorded records.Entry) ([]byte, bool, error) {
		var err error
		if previous, err = registrationOf(recorded); err != nil && !isUndecodable(err) {
			return nil, false, err
		}
		return encoded, true, nil
	})
	if errors.Is(err, records.ErrContended) {
		return Registration{}, fmt.Errorf("%s's env source registration was rewritten under every attempt to record it; another deploy of it is still running", registration.Project)
	}
	if err != nil {
		return Registration{}, err
	}
	return registration, moveSharer(ctx, store.KeyValues, tier, registration.Project, previous.DedupeKey, key)
}

func RestoreRegistration(ctx context.Context, store variablestore.Store, tier environment.Tier, replacing Registration, previous *Registration) error {
	name := registrationKey(tier, replacing.Project)
	recorded, err := store.KeyValues.Read(ctx, name)
	if errors.Is(err, records.ErrNotFound) {
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
		if errors.Is(err, records.ErrStale) || errors.Is(err, records.ErrNotFound) {
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
	if errors.Is(err, records.ErrStale) {
		return nil
	}
	if err != nil {
		return err
	}
	return moveSharer(ctx, store.KeyValues, tier, replacing.Project, replacing.DedupeKey, previous.DedupeKey)
}

func rekey(ctx context.Context, store variablestore.Store, tier environment.Tier, project string) error {
	recorded, err := store.KeyValues.Read(ctx, registrationKey(tier, project))
	if errors.Is(err, records.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := registrationOf(recorded)
	if isUndecodable(err) {
		return nil
	}
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
	if errors.Is(err, records.ErrStale) {
		return nil
	}
	if err != nil {
		return err
	}
	return moveSharer(ctx, store.KeyValues, tier, project, previous, key)
}

func registrationOf(recorded records.Entry) (Registration, error) {
	if len(recorded.Value) == 0 {
		return Registration{}, nil
	}
	var envelope recordedRegistration
	if err := json.Unmarshal(recorded.Value, &envelope); err != nil {
		return Registration{}, fmt.Errorf("read %s: %w", recorded.Key, err)
	}
	out := Registration{Project: envelope.Project, Folders: envelope.Folders, DedupeKey: envelope.DedupeKey}
	var stored storedDescriptor
	err := json.Unmarshal(envelope.Descriptor, &stored)
	if err == nil {
		out.Descriptor, err = NewDescriptor(stored.Kind, stored.Options)
	}
	if err != nil {
		return out, &UndecodableRegistrationError{Project: envelope.Project, Kind: stored.Kind, Err: err}
	}
	return out, nil
}

func Registered(ctx context.Context, store records.Store, tier environment.Tier, project string) (Registration, bool, error) {
	recorded, err := store.Read(ctx, registrationKey(tier, project))
	if errors.Is(err, records.ErrNotFound) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	out, err := registrationOf(recorded)
	if err != nil && !isUndecodable(err) {
		return Registration{}, false, err
	}
	return out, true, err
}

func Registrations(ctx context.Context, store records.Store, tier environment.Tier) ([]Registration, error) {
	registrations, _, err := readRegistrations(ctx, store, tier)
	return registrations, err
}

func readRegistrations(ctx context.Context, store records.Store, tier environment.Tier) ([]Registration, []records.Entry, error) {
	recorded, err := store.List(ctx, registrationsPartition(tier))
	if err != nil {
		return nil, nil, fmt.Errorf("read the %s env source registrations: %w", tier, err)
	}
	out := make([]Registration, 0, len(recorded))
	var removals []records.Entry
	var undecodable []error
	for _, entry := range recorded {
		if len(entry.Key.Path) > 1 {
			if entry.Key.Path[1] == removalSegment {
				removals = append(removals, entry)
			}
			continue
		}
		registration, err := registrationOf(entry)
		if isUndecodable(err) {
			undecodable = append(undecodable, err)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, registration)
	}
	slices.SortFunc(out, func(a, b Registration) int { return strings.Compare(a.Project, b.Project) })
	return out, removals, errors.Join(undecodable...)
}

func ScheduledProjects(ctx context.Context, store records.Store, tier environment.Tier) ([]string, error) {
	registrations, err := Registrations(ctx, store, tier)
	if registrations == nil {
		return nil, err
	}
	projects := []string{}
	for _, registration := range registrations {
		if registration.Descriptor.Reading() == ReadingOnSchedule {
			projects = append(projects, registration.Project)
		}
	}
	return projects, nil
}
