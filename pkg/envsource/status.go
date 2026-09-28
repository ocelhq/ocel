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
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/keyvalue"
)

const (
	statusAttempts      = 3
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

func statusPartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvSourceStatus}
}

func statusSegment(dedupeKey string) string {
	sum := sha256.Sum256([]byte(dedupeKey))
	return hex.EncodeToString(sum[:statusNameHashBytes])
}

func statusKey(tier environment.Tier, dedupeKey string) keyvalue.Key {
	return statusPartition(tier).Key(statusSegment(dedupeKey))
}

func sharerKey(tier environment.Tier, dedupeKey, project string) keyvalue.Key {
	return statusPartition(tier).Key(statusSegment(dedupeKey), sharersSegment, project)
}

type sharer struct {
	Project string `json:"project"`
}

func StatusOf(ctx context.Context, store envvars.Store, tier environment.Tier, registration Registration) (Status, error) {
	key, err := DedupeKey(ctx, store, envvars.Scope{Project: registration.Project, Tier: tier}, registration.Descriptor)
	if err != nil {
		return Status{}, err
	}
	status, _, err := readStatus(ctx, store.KeyValues, tier, key)
	return status, err
}

func readStatus(ctx context.Context, store keyvalue.Store, tier environment.Tier, dedupeKey string) (Status, keyvalue.Entry, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, statusKey(tier, dedupeKey))
	if err != nil {
		return Status{}, keyvalue.Entry{}, err
	}
	var status Status
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &status); err != nil {
			return Status{}, keyvalue.Entry{}, fmt.Errorf("read %s: %w", recorded.Key, err)
		}
	}
	return status, recorded, nil
}

func writeStatus(ctx context.Context, store keyvalue.Store, tier environment.Tier, dedupeKey string, update func(*Status)) error {
	for range statusAttempts {
		status, recorded, err := readStatus(ctx, store, tier, dedupeKey)
		if err != nil {
			return err
		}
		update(&status)
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		created := recorded.Revision == ""
		recorded.Value = encoded
		_, err = store.Write(ctx, recorded)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil || !created {
			return err
		}
		return forgetUnshared(ctx, store, tier, dedupeKey)
	}
	return fmt.Errorf("the %s env source status was rewritten under each of %d attempts to record this sync, so this sync's outcome is lost", tier, statusAttempts)
}

func ForgetProject(ctx context.Context, store envvars.Store, tier environment.Tier, project string) error {
	registration, registered, err := Registered(ctx, store.KeyValues, tier, project)
	if err != nil || !registered {
		return err
	}
	if err := release(ctx, store.KeyValues, tier, registration.DedupeKey, project); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, store.KeyValues, registrationKey(tier, project))
}

func moveSharer(ctx context.Context, store keyvalue.Store, tier environment.Tier, project, from, to string) error {
	marker, err := keyvalue.ReadOrEmpty(ctx, store, sharerKey(tier, to, project))
	if err != nil {
		return err
	}
	if marker.Revision == "" {
		if marker.Value, err = json.Marshal(sharer{Project: project}); err != nil {
			return err
		}
		if _, err := store.Write(ctx, marker); err != nil && !errors.Is(err, keyvalue.ErrStale) {
			return err
		}
	}
	if from == "" || from == to {
		return nil
	}
	return release(ctx, store, tier, from, project)
}

func release(ctx context.Context, store keyvalue.Store, tier environment.Tier, dedupeKey, project string) error {
	if dedupeKey == "" {
		return nil
	}
	if err := keyvalue.Forget(ctx, store, sharerKey(tier, dedupeKey, project)); err != nil {
		return err
	}
	return forgetUnshared(ctx, store, tier, dedupeKey)
}

func forgetUnshared(ctx context.Context, store keyvalue.Store, tier environment.Tier, dedupeKey string) error {
	sharers, err := store.List(ctx, statusPartition(tier), statusSegment(dedupeKey), sharersSegment)
	if err != nil || len(sharers) > 0 {
		return err
	}
	return keyvalue.Forget(ctx, store, statusKey(tier, dedupeKey))
}
