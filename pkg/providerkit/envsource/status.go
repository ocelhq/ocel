package envsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const statusAttempts = 3

type Status struct {
	Source        string `json:"source"`
	LastAttemptAt int64  `json:"lastAttemptAt,omitempty"`
	LastSuccessAt int64  `json:"lastSuccessAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	Failures      int    `json:"failures,omitempty"`
	RetryAt       int64  `json:"retryAt,omitempty"`
}

func statusName(class ports.Class, identity string) ports.RecordName {
	sum := sha256.Sum256([]byte(identity))
	return ports.RecordName{ports.RootEnvSyncs, string(class), hex.EncodeToString(sum[:16])}
}

func StatusOf(ctx context.Context, store values.Store, class ports.Class, registration Registration) (Status, error) {
	identity := Identity(ctx, store, values.Scope{Project: registration.Project, Class: class}, registration.Descriptor)
	status, _, err := readStatus(ctx, store.Records, class, identity)
	return status, err
}

func readStatus(ctx context.Context, records ports.RecordStore, class ports.Class, identity string) (Status, ports.Record, error) {
	held, err := ports.Held(ctx, records, statusName(class, identity))
	if err != nil {
		return Status{}, ports.Record{}, err
	}
	var status Status
	if len(held.Bytes) > 0 {
		if err := json.Unmarshal(held.Bytes, &status); err != nil {
			return Status{}, ports.Record{}, fmt.Errorf("read %s: %w", held.Name, err)
		}
	}
	return status, held, nil
}

func recordStatus(ctx context.Context, records ports.RecordStore, class ports.Class, identity string, update func(*Status)) error {
	for range statusAttempts {
		status, held, err := readStatus(ctx, records, class, identity)
		if err != nil {
			return err
		}
		update(&status)
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		held.Bytes = encoded
		_, err = records.Write(ctx, held)
		if !errors.Is(err, ports.ErrStale) {
			return err
		}
	}
	return nil
}

func Retire(ctx context.Context, store values.Store, class ports.Class, project string) error {
	registration, registered, err := Registered(ctx, store.Records, class, project)
	if err != nil || !registered {
		return err
	}
	identity := Identity(ctx, store, values.Scope{Project: project, Class: class}, registration.Descriptor)
	if err := Unregister(ctx, store.Records, class, project); err != nil {
		return err
	}
	others, err := Registrations(ctx, store.Records, class)
	if err != nil {
		return err
	}
	for _, other := range others {
		if Identity(ctx, store, values.Scope{Project: other.Project, Class: class}, other.Descriptor) == identity {
			return nil
		}
	}
	return ports.Forget(ctx, store.Records, statusName(class, identity))
}
