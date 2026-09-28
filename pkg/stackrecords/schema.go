package stackrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const SchemaVersion = 2

const schemaAttempts = 8

func EnsureSchema(ctx context.Context, store keyvalue.Store, tier environment.Tier) error {
	for range schemaAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, store, SchemaKey(tier))
		if err != nil {
			return fmt.Errorf("read the record schema: %w", err)
		}
		written, err := schemaVersionOf(recorded)
		if err != nil {
			return err
		}
		switch {
		case written == SchemaVersion:
			return nil
		case written > SchemaVersion:
			return refusal.Refuse(refusal.CodeNotReady,
				"this account's records are at schema %d and this build reads schema %d: a newer ocel wrote them, and migrations run forward only. Update ocel rather than downgrade the records",
				written, SchemaVersion)
		}
		if recorded.Value, err = json.Marshal(SchemaVersion); err != nil {
			return fmt.Errorf("record the record schema: %w", err)
		}
		if _, err := store.Write(ctx, recorded); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return fmt.Errorf("record the record schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("record the record schema: it moved under %d attempts", schemaAttempts)
}

func WrittenSchema(ctx context.Context, store keyvalue.Store, tier environment.Tier) (int, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, SchemaKey(tier))
	if err != nil {
		return 0, fmt.Errorf("read the record schema: %w", err)
	}
	return schemaVersionOf(recorded)
}

func schemaVersionOf(recorded keyvalue.Entry) (int, error) {
	if len(recorded.Value) == 0 {
		return 0, nil
	}
	var written int
	if err := json.Unmarshal(recorded.Value, &written); err != nil {
		return 0, fmt.Errorf("read the record schema: %s is not a schema version", recorded.Value)
	}
	return written, nil
}
