package envsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func statusName(class edge.Class, dedupeKey string) records.Name {
	sum := sha256.Sum256([]byte(dedupeKey))
	return records.Name{records.RootEnvSourceStatus, string(class), hex.EncodeToString(sum[:statusNameHashBytes])}
}

func sharersName(class edge.Class, dedupeKey string) records.Name {
	return append(statusName(class, dedupeKey), sharersSegment)
}

func StatusOf(ctx context.Context, store envvars.Store, class edge.Class, registration Registration) (Status, error) {
	key, err := DedupeKey(ctx, store, envvars.Scope{Project: registration.Project, Class: class}, registration.Descriptor)
	if err != nil {
		return Status{}, err
	}
	status, _, err := readStatus(ctx, store.Records, class, key)
	return status, err
}

func readStatus(ctx context.Context, store records.Store, class edge.Class, dedupeKey string) (Status, records.Record, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, statusName(class, dedupeKey))
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

func writeStatus(ctx context.Context, store records.Store, class edge.Class, dedupeKey string, update func(*Status)) error {
	for range statusAttempts {
		status, recorded, err := readStatus(ctx, store, class, dedupeKey)
		if err != nil {
			return err
		}
		update(&status)
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		recorded.Bytes = encoded
		_, err = store.Write(ctx, recorded)
		if !errors.Is(err, records.ErrStale) {
			return err
		}
	}
	return fmt.Errorf("the %s env source status was rewritten under each of %d attempts to record this sync, so this sync's outcome is lost", class, statusAttempts)
}

func ForgetProject(ctx context.Context, store envvars.Store, class edge.Class, project string) error {
	registration, registered, err := Registered(ctx, store.Records, class, project)
	if err != nil || !registered {
		return err
	}
	if err := records.Forget(ctx, store.Records, registrationName(class, project)); err != nil {
		return err
	}
	return release(ctx, store.Records, class, registration.DedupeKey, project)
}

func moveSharer(ctx context.Context, store records.Store, class edge.Class, project, from, to string) error {
	marker, err := records.ReadOrEmpty(ctx, store, append(sharersName(class, to), project))
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
	return release(ctx, store, class, from, project)
}

func release(ctx context.Context, store records.Store, class edge.Class, dedupeKey, project string) error {
	if dedupeKey == "" {
		return nil
	}
	if err := records.Forget(ctx, store, append(sharersName(class, dedupeKey), project)); err != nil {
		return err
	}
	sharers, err := store.List(ctx, sharersName(class, dedupeKey))
	if err != nil || len(sharers) > 0 {
		return err
	}
	return records.Forget(ctx, store, statusName(class, dedupeKey))
}
