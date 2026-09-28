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
	"github.com/ocelhq/ocel/pkg/records"
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

func statusName(tier environment.Tier, dedupeKey string) records.Name {
	sum := sha256.Sum256([]byte(dedupeKey))
	return records.Name{records.RootEnvSourceStatus, string(tier), hex.EncodeToString(sum[:statusNameHashBytes])}
}

func sharersName(tier environment.Tier, dedupeKey string) records.Name {
	return append(statusName(tier, dedupeKey), sharersSegment)
}

func StatusOf(ctx context.Context, store envvars.Store, tier environment.Tier, registration Registration) (Status, error) {
	key, err := DedupeKey(ctx, store, envvars.Scope{Project: registration.Project, Tier: tier}, registration.Descriptor)
	if err != nil {
		return Status{}, err
	}
	status, _, err := readStatus(ctx, store.Records, tier, key)
	return status, err
}

func readStatus(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string) (Status, records.Record, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, statusName(tier, dedupeKey))
	if err != nil {
		return Status{}, records.Record{}, err
	}
	var status Status
	if len(recorded.Bytes) > 0 {
		if err := json.Unmarshal(recorded.Bytes, &status); err != nil {
			return Status{}, records.Record{}, fmt.Errorf("read %s: %w", recorded.Name, err)
		}
	}
	return status, recorded, nil
}

func writeStatus(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string, update func(*Status)) error {
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
		recorded.Bytes = encoded
		_, err = store.Write(ctx, recorded)
		if errors.Is(err, records.ErrStale) {
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
	registration, registered, err := Registered(ctx, store.Records, tier, project)
	if err != nil || !registered {
		return err
	}
	if err := release(ctx, store.Records, tier, registration.DedupeKey, project); err != nil {
		return err
	}
	return records.Forget(ctx, store.Records, registrationName(tier, project))
}

func moveSharer(ctx context.Context, store records.Store, tier environment.Tier, project, from, to string) error {
	marker, err := records.ReadOrEmpty(ctx, store, append(sharersName(tier, to), project))
	if err != nil {
		return err
	}
	if marker.Revision == "" {
		marker.Bytes = []byte(project)
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
	if err := records.Forget(ctx, store, append(sharersName(tier, dedupeKey), project)); err != nil {
		return err
	}
	return forgetUnshared(ctx, store, tier, dedupeKey)
}

func forgetUnshared(ctx context.Context, store records.Store, tier environment.Tier, dedupeKey string) error {
	sharers, err := store.List(ctx, sharersName(tier, dedupeKey))
	if err != nil || len(sharers) > 0 {
		return err
	}
	return records.Forget(ctx, store, statusName(tier, dedupeKey))
}
