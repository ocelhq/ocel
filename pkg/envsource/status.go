package envsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const (
	statusNameHashBytes = 16
	sharersSegment      = "projects"
)

type Status struct {
	EnvSource           string            `json:"envSource"`
	LastAttemptAt       time.Time         `json:"lastAttemptAt,omitzero"`
	LastSuccessAt       time.Time         `json:"lastSuccessAt,omitzero"`
	LastError           string            `json:"lastError,omitempty"`
	ConsecutiveFailures int               `json:"consecutiveFailures,omitempty"`
	RetryAt             time.Time         `json:"retryAt,omitzero"`
	URLs                map[string]string `json:"urls,omitempty"`
}

func statusPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootStatus}
}

func statusSegment(dedupeKey string) string {
	sum := sha256.Sum256([]byte(dedupeKey))
	return hex.EncodeToString(sum[:statusNameHashBytes])
}

func statusKey(tier environment.Tier, dedupeKey string) records.Key {
	return statusPartition(tier).Key(statusSegment(dedupeKey))
}

func sharerKey(tier environment.Tier, dedupeKey, project string) records.Key {
	return statusPartition(tier).Key(statusSegment(dedupeKey), sharersSegment, project)
}

type sharer struct {
	Project string `json:"project"`
}

func StatusOf(ctx context.Context, store variablestore.Store, tier environment.Tier, registration Registration) (Status, error) {
	key, err := DedupeKey(ctx, store, variablestore.Scope{Project: registration.Project, Tier: tier}, registration.Descriptor)
	if err != nil {
		return Status{}, err
	}
	status, _, err := readStatus(ctx, store.KeyValues, tier, key)
	return status, err
}

func readStatus(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string) (Status, records.Entry, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, statusKey(tier, dedupeKey))
	if err != nil {
		return Status{}, records.Entry{}, err
	}
	status, err := decodeStatus(recorded)
	if err != nil {
		return Status{}, records.Entry{}, err
	}
	return status, recorded, nil
}

func decodeStatus(recorded records.Entry) (Status, error) {
	var status Status
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &status); err != nil {
			return Status{}, fmt.Errorf("read %s: %w", recorded.Key, err)
		}
	}
	return status, nil
}

func writeStatus(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string, update func(*Status)) error {
	created := false
	err := records.Change(ctx, store, statusKey(tier, dedupeKey), func(recorded records.Entry) ([]byte, bool, error) {
		status, err := decodeStatus(recorded)
		if err != nil {
			return nil, false, err
		}
		update(&status)
		encoded, err := json.Marshal(status)
		created = recorded.Revision == ""
		return encoded, true, err
	})
	if errors.Is(err, records.ErrContended) {
		return fmt.Errorf("the %s env source status was rewritten under every attempt to record this sync, so this sync's outcome is lost", tier)
	}
	if err != nil || !created {
		return err
	}
	return forgetUnshared(ctx, store, tier, dedupeKey)
}

func ForgetProject(ctx context.Context, store variablestore.Store, tier environment.Tier, project string) error {
	registration, registered, err := Registered(ctx, store.KeyValues, tier, project)
	if (err != nil && !isUndecodable(err)) || !registered {
		return err
	}
	if err == nil && registration.Descriptor.Reading() == ReadingOnSchedule {
		if err := recordRemoval(ctx, store.KeyValues, tier, project); err != nil {
			return err
		}
	}
	return unregister(ctx, store.KeyValues, tier, project, registration.DedupeKey)
}

func Unregister(ctx context.Context, store variablestore.Store, tier environment.Tier, project string) error {
	registration, registered, err := Registered(ctx, store.KeyValues, tier, project)
	if (err != nil && !isUndecodable(err)) || !registered {
		return err
	}
	return unregister(ctx, store.KeyValues, tier, project, registration.DedupeKey)
}

func unregister(ctx context.Context, store records.Store, tier environment.Tier, project, dedupeKey string) error {
	if err := release(ctx, store, tier, dedupeKey, project); err != nil {
		return err
	}
	return records.Forget(ctx, store, registrationKey(tier, project))
}

func moveSharer(ctx context.Context, store records.Store, tier environment.Tier, project, from, to string) error {
	marker, err := records.ReadOrEmpty(ctx, store, sharerKey(tier, to, project))
	if err != nil {
		return err
	}
	if marker.Revision == "" {
		if marker.Value, err = json.Marshal(sharer{Project: project}); err != nil {
			return err
		}
		if _, err := store.Write(ctx, marker); err != nil && !errors.Is(err, records.ErrStale) {
			return err
		}
	}
	if from == "" || from == to {
		return nil
	}
	return release(ctx, store, tier, from, project)
}

func release(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey, project string) error {
	if dedupeKey == "" {
		return nil
	}
	if err := records.Forget(ctx, store, sharerKey(tier, dedupeKey, project)); err != nil {
		return err
	}
	return forgetUnshared(ctx, store, tier, dedupeKey)
}

func forgetUnshared(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string) error {
	sharers, err := store.List(ctx, statusPartition(tier), statusSegment(dedupeKey), sharersSegment)
	if err != nil || len(sharers) > 0 {
		return err
	}
	return records.Forget(ctx, store, statusKey(tier, dedupeKey))
}

func ownerOfStatus(path []string) (records.Owner, bool) {
	switch {
	case len(path) == 1:
		return records.Owner{Shared: true}, true
	case len(path) == 3 && path[1] == sharersSegment:
		return records.Owner{Project: path[2]}, true
	}
	return records.Owner{}, false
}
