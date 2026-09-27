package envsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const registerAttempts = 5

type Registration struct {
	Project    string     `json:"project"`
	Descriptor Descriptor `json:"descriptor"`
	Folders    []string   `json:"folders"`
	DedupeKey  string     `json:"dedupeKey"`
}

func (r Registration) Credentials() []envvars.Cell {
	var out []envvars.Cell
	for _, name := range r.Descriptor.CredentialVariables() {
		out = append(out, envvars.Cell{Key: name})
	}
	return out
}

func registrationName(class edge.Class, project string) records.Name {
	return records.Name{records.RootEnvSources, string(class), project}
}

func Register(ctx context.Context, store envvars.Store, class edge.Class, registration Registration) (Registration, error) {
	key, err := DedupeKey(ctx, store, envvars.Scope{Project: registration.Project, Class: class}, registration.Descriptor)
	if err != nil {
		return Registration{}, err
	}
	registration.DedupeKey = key
	registration.Folders = slices.Compact(slices.Sorted(slices.Values(registration.Folders)))
	encoded, err := json.Marshal(registration)
	if err != nil {
		return Registration{}, err
	}
	name := registrationName(class, registration.Project)
	for range registerAttempts {
		recorded, err := records.ReadOrEmpty(ctx, store.Records, name)
		if err != nil {
			return Registration{}, err
		}
		previous, err := registrationOf(recorded)
		if err != nil {
			return Registration{}, err
		}
		recorded.Bytes = encoded
		_, err = store.Records.Write(ctx, recorded)
		if errors.Is(err, records.ErrStale) {
			continue
		}
		if err != nil {
			return Registration{}, err
		}
		return registration, moveSharer(ctx, store.Records, class, registration.Project, previous.DedupeKey, key)
	}
	return Registration{}, fmt.Errorf("%s's env source registration was rewritten under every attempt to record it; another deploy of it is still running", registration.Project)
}

func rekey(ctx context.Context, store envvars.Store, class edge.Class, project string) error {
	recorded, err := store.Records.Read(ctx, registrationName(class, project))
	if errors.Is(err, records.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := registrationOf(recorded)
	if err != nil {
		return err
	}
	key, err := DedupeKey(ctx, store, envvars.Scope{Project: project, Class: class}, current.Descriptor)
	if err != nil || key == current.DedupeKey {
		return err
	}
	previous := current.DedupeKey
	current.DedupeKey = key
	if recorded.Bytes, err = json.Marshal(current); err != nil {
		return err
	}
	_, err = store.Records.Write(ctx, recorded)
	if errors.Is(err, records.ErrStale) {
		return nil
	}
	if err != nil {
		return err
	}
	return moveSharer(ctx, store.Records, class, project, previous, key)
}

func registrationOf(recorded records.Record) (Registration, error) {
	var out Registration
	if len(recorded.Bytes) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(recorded.Bytes, &out); err != nil {
		return Registration{}, fmt.Errorf("read %s: %w", recorded.Name, err)
	}
	return out, nil
}

func Registered(ctx context.Context, store records.Store, class edge.Class, project string) (Registration, bool, error) {
	recorded, err := store.Read(ctx, registrationName(class, project))
	if errors.Is(err, records.ErrNotFound) {
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

func Registrations(ctx context.Context, store records.Store, class edge.Class) ([]Registration, error) {
	recorded, err := store.List(ctx, records.Name{records.RootEnvSources, string(class)})
	if err != nil {
		return nil, fmt.Errorf("read the %s env source registrations: %w", class, err)
	}
	out := make([]Registration, 0, len(recorded))
	for _, record := range recorded {
		var registration Registration
		if err := json.Unmarshal(record.Bytes, &registration); err != nil {
			return nil, fmt.Errorf("read %s: %w", record.Name, err)
		}
		out = append(out, registration)
	}
	slices.SortFunc(out, func(a, b Registration) int { return strings.Compare(a.Project, b.Project) })
	return out, nil
}
