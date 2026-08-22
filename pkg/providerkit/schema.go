package providerkit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

const RecordSchemaVersion = 1

const schemaAttempts = 8

func EnsureRecordSchema(ctx context.Context, records RecordStore) error {
	for range schemaAttempts {
		held, err := Held(ctx, records, SchemaRecord())
		if err != nil {
			return fmt.Errorf("read the record schema: %w", err)
		}
		written, err := schemaVersionOf(held)
		if err != nil {
			return err
		}
		if written > RecordSchemaVersion {
			return Refuse(CodeNotReady,
				"this account's records are at schema %d and this build reads schema %d: a newer ocel wrote them, and migrations run forward only. Update ocel rather than downgrade the records",
				written, RecordSchemaVersion)
		}
		if written == RecordSchemaVersion {
			return nil
		}
		held.Bytes = []byte(strconv.Itoa(RecordSchemaVersion))
		if _, err := records.Write(ctx, held); err != nil {
			if errors.Is(err, ErrStale) {
				continue
			}
			return fmt.Errorf("record the record schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("record the record schema: it moved under %d attempts", schemaAttempts)
}

func RecordSchema(ctx context.Context, records RecordStore) (int, error) {
	held, err := Held(ctx, records, SchemaRecord())
	if err != nil {
		return 0, fmt.Errorf("read the record schema: %w", err)
	}
	return schemaVersionOf(held)
}

func schemaVersionOf(held Record) (int, error) {
	if len(held.Bytes) == 0 {
		return 0, nil
	}
	written, err := strconv.Atoi(string(held.Bytes))
	if err != nil {
		return 0, fmt.Errorf("read the record schema: %q is not a schema version", held.Bytes)
	}
	return written, nil
}
