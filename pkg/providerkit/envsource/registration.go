package envsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const registerAttempts = 5

type Registration struct {
	Project    string     `json:"project"`
	Descriptor Descriptor `json:"descriptor"`
	Folders    []string   `json:"folders"`
}

func (r Registration) Credentials() []values.Cell {
	if r.Descriptor.Infisical == nil {
		return nil
	}
	var out []values.Cell
	for _, name := range r.Descriptor.Infisical.Auth.Vars() {
		out = append(out, values.Cell{Key: name})
	}
	return out
}

func registrationName(class ports.Class, project string) ports.RecordName {
	return ports.RecordName{ports.RootEnvSources, string(class), project}
}

func Register(ctx context.Context, records ports.RecordStore, class ports.Class, registration Registration) error {
	registration.Folders = slices.Sorted(slices.Values(registration.Folders))
	registration.Folders = slices.Compact(registration.Folders)
	encoded, err := json.Marshal(registration)
	if err != nil {
		return err
	}
	name := registrationName(class, registration.Project)
	for range registerAttempts {
		held, err := ports.Held(ctx, records, name)
		if err != nil {
			return err
		}
		held.Bytes = encoded
		_, err = records.Write(ctx, held)
		if !errors.Is(err, ports.ErrStale) {
			return err
		}
	}
	return fmt.Errorf("%s's env source registration was rewritten under every attempt to record it; another deploy of it is still running", registration.Project)
}

func Unregister(ctx context.Context, records ports.RecordStore, class ports.Class, project string) error {
	return ports.Forget(ctx, records, registrationName(class, project))
}

func Registered(ctx context.Context, records ports.RecordStore, class ports.Class, project string) (Registration, bool, error) {
	held, err := records.Read(ctx, registrationName(class, project))
	if errors.Is(err, ports.ErrNoRecord) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	var out Registration
	if err := json.Unmarshal(held.Bytes, &out); err != nil {
		return Registration{}, false, fmt.Errorf("read %s's env source registration: %w", project, err)
	}
	return out, true, nil
}

func Registrations(ctx context.Context, records ports.RecordStore, class ports.Class) ([]Registration, error) {
	held, err := records.List(ctx, ports.RecordName{ports.RootEnvSources, string(class)})
	if err != nil {
		return nil, fmt.Errorf("read the %s env source registrations: %w", class, err)
	}
	out := make([]Registration, 0, len(held))
	for _, record := range held {
		var registration Registration
		if err := json.Unmarshal(record.Bytes, &registration); err != nil {
			return nil, fmt.Errorf("read %s: %w", record.Name, err)
		}
		out = append(out, registration)
	}
	slices.SortFunc(out, func(a, b Registration) int { return strings.Compare(a.Project, b.Project) })
	return out, nil
}
