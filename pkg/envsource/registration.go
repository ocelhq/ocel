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

func Register(ctx context.Context, store records.Store, class edge.Class, registration Registration) error {
	registration.Folders = slices.Compact(slices.Sorted(slices.Values(registration.Folders)))
	encoded, err := json.Marshal(registration)
	if err != nil {
		return err
	}
	name := registrationName(class, registration.Project)
	for range registerAttempts {
		recorded, err := records.ReadOrEmpty(ctx, store, name)
		if err != nil {
			return err
		}
		recorded.Bytes = encoded
		_, err = store.Write(ctx, recorded)
		if !errors.Is(err, records.ErrStale) {
			return err
		}
	}
	return fmt.Errorf("%s's env source registration was rewritten under every attempt to record it; another deploy of it is still running", registration.Project)
}

func Unregister(ctx context.Context, store records.Store, class edge.Class, project string) error {
	return records.Forget(ctx, store, registrationName(class, project))
}

func Registered(ctx context.Context, store records.Store, class edge.Class, project string) (Registration, bool, error) {
	recorded, err := store.Read(ctx, registrationName(class, project))
	if errors.Is(err, records.ErrNotFound) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	var out Registration
	if err := json.Unmarshal(recorded.Bytes, &out); err != nil {
		return Registration{}, false, fmt.Errorf("read %s's env source registration: %w", project, err)
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
