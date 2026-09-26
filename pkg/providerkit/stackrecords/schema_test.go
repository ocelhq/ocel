package stackrecords_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTheRootSchemaIsWrittenOnceAndReadBack(t *testing.T) {
	store := fake.NewRecords()
	ctx := context.Background()

	if written, err := stackrecords.WrittenSchema(ctx, store, edge.ClassProduction); err != nil || written != 0 {
		t.Fatalf("WrittenSchema() of an unwritten tree = %d, %v, want 0", written, err)
	}
	if err := stackrecords.EnsureSchema(ctx, store, edge.ClassProduction); err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Read(ctx, stackrecords.SchemaRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.EnsureSchema(ctx, store, edge.ClassProduction); err != nil {
		t.Fatalf("a second EnsureSchema() = %v, want it a no-op", err)
	}
	again, err := store.Read(ctx, stackrecords.SchemaRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != recorded.Revision {
		t.Fatalf("the schema record was rewritten at revision %q, want the %q already written", again.Revision, recorded.Revision)
	}
	if written, err := stackrecords.WrittenSchema(ctx, store, edge.ClassProduction); err != nil || written != stackrecords.SchemaVersion {
		t.Fatalf("WrittenSchema() = %d, %v, want %d", written, err, stackrecords.SchemaVersion)
	}
}

func TestARecordTreeAnOlderOcelWroteIsRefused(t *testing.T) {
	store := fake.NewRecords()
	ctx := context.Background()

	behind := strconv.Itoa(stackrecords.SchemaVersion - 1)
	if _, err := store.Write(ctx, records.Record{Name: stackrecords.SchemaRecord(edge.ClassProduction), Bytes: []byte(behind)}); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.EnsureSchema(ctx, store, edge.ClassProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureSchema() over an older tree = %v, want it refused as not ready", err)
	}
	if !strings.Contains(refused.Message, behind) || !strings.Contains(refused.Message, strconv.Itoa(stackrecords.SchemaVersion)) {
		t.Errorf("refusal = %q, want it to name both the schema written and the schema this build reads", refused.Message)
	}
	recorded, err := store.Read(ctx, stackrecords.SchemaRecord(edge.ClassProduction))
	if err != nil || string(recorded.Bytes) != behind {
		t.Fatalf("the refused tree was stamped %q, want it left at %q rather than claimed as this build's", recorded.Bytes, behind)
	}
}

func TestARecordTreeANewerOcelWroteIsRefused(t *testing.T) {
	store := fake.NewRecords()
	ctx := context.Background()

	ahead := strconv.Itoa(stackrecords.SchemaVersion + 1)
	if _, err := store.Write(ctx, records.Record{Name: stackrecords.SchemaRecord(edge.ClassProduction), Bytes: []byte(ahead)}); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.EnsureSchema(ctx, store, edge.ClassProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureSchema() over a newer tree = %v, want it refused as not ready", err)
	}
	recorded, err := store.Read(ctx, stackrecords.SchemaRecord(edge.ClassProduction))
	if err != nil || string(recorded.Bytes) != ahead {
		t.Fatalf("the refused downgrade left %q behind, want the tree untouched at %q", recorded.Bytes, ahead)
	}
}
