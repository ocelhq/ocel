package stackrecords

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const SchemaVersion = 2

const schemaAttempts = 8

func EnsureSchema(ctx context.Context, store records.Store, class edge.Class) error {
	for range schemaAttempts {
		recorded, err := records.ReadOrEmpty(ctx, store, SchemaRecord(class))
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
		case written > 0:
			return refusal.Refuse(refusal.CodeNotReady,
				"this account's records are at schema %d and this build reads schema %d: an older ocel wrote them under a layout this build does not read, and there is no migration between the two. Remove what that ocel deployed with the ocel that deployed it, then bootstrap this account afresh",
				written, SchemaVersion)
		}
		recorded.Bytes = []byte(strconv.Itoa(SchemaVersion))
		if _, err := store.Write(ctx, recorded); err != nil {
			if errors.Is(err, records.ErrStale) {
				continue
			}
			return fmt.Errorf("record the record schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("record the record schema: it moved under %d attempts", schemaAttempts)
}

func WrittenSchema(ctx context.Context, store records.Store, class edge.Class) (int, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, SchemaRecord(class))
	if err != nil {
		return 0, fmt.Errorf("read the record schema: %w", err)
	}
	return schemaVersionOf(recorded)
}

func schemaVersionOf(recorded records.Record) (int, error) {
	if len(recorded.Bytes) == 0 {
		return 0, nil
	}
	written, err := strconv.Atoi(string(recorded.Bytes))
	if err != nil {
		return 0, fmt.Errorf("read the record schema: %q is not a schema version", recorded.Bytes)
	}
	return written, nil
}
