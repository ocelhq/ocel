package stackrecords_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestTheRootSchemaIsWrittenOnceAndReadBack(t *testing.T) {
	store := fake.NewKeyValues()
	ctx := context.Background()

	if written, err := stackrecords.WrittenSchema(ctx, store, environment.TierProduction); err != nil || written != 0 {
		t.Fatalf("WrittenSchema() of an unwritten tree = %d, %v, want 0", written, err)
	}
	if err := stackrecords.EnsureSchema(ctx, store, environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Read(ctx, stackrecords.SchemaKey(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.EnsureSchema(ctx, store, environment.TierProduction); err != nil {
		t.Fatalf("a second EnsureSchema() = %v, want it a no-op", err)
	}
	again, err := store.Read(ctx, stackrecords.SchemaKey(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != recorded.Revision {
		t.Fatalf("the schema record was rewritten at revision %q, want the %q already written", again.Revision, recorded.Revision)
	}
	if written, err := stackrecords.WrittenSchema(ctx, store, environment.TierProduction); err != nil || written != stackrecords.SchemaVersion {
		t.Fatalf("WrittenSchema() = %d, %v, want %d", written, err, stackrecords.SchemaVersion)
	}
	var version int
	if err := json.Unmarshal(again.Value, &version); err != nil || version != stackrecords.SchemaVersion {
		t.Fatalf("the schema entry holds %s, want the JSON number %d a reader of the store can see", again.Value, stackrecords.SchemaVersion)
	}
}

func TestARecordTreeStampedBelowThisBuildsSchemaIsStampedAtIt(t *testing.T) {
	store := fake.NewKeyValues()
	ctx := context.Background()

	behind := strconv.Itoa(stackrecords.SchemaVersion - 1)
	if _, err := store.Write(ctx, keyvalue.Entry{Key: stackrecords.SchemaKey(environment.TierProduction), Value: json.RawMessage(behind)}); err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.EnsureSchema(ctx, store, environment.TierProduction); err != nil {
		t.Fatalf("EnsureSchema() over a tree stamped %s = %v, want it stamped at %d", behind, err, stackrecords.SchemaVersion)
	}
	if written, err := stackrecords.WrittenSchema(ctx, store, environment.TierProduction); err != nil || written != stackrecords.SchemaVersion {
		t.Fatalf("WrittenSchema() = %d, %v, want %d", written, err, stackrecords.SchemaVersion)
	}
}

func TestARecordTreeANewerOcelWroteIsRefused(t *testing.T) {
	store := fake.NewKeyValues()
	ctx := context.Background()

	ahead := strconv.Itoa(stackrecords.SchemaVersion + 1)
	if _, err := store.Write(ctx, keyvalue.Entry{Key: stackrecords.SchemaKey(environment.TierProduction), Value: json.RawMessage(ahead)}); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.EnsureSchema(ctx, store, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureSchema() over a newer tree = %v, want it refused as not ready", err)
	}
	recorded, err := store.Read(ctx, stackrecords.SchemaKey(environment.TierProduction))
	if err != nil || string(recorded.Value) != ahead {
		t.Fatalf("the refused downgrade left %q behind, want the tree untouched at %q", recorded.Value, ahead)
	}
}
